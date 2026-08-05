package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

var (
	errInitialEnrichmentNilContext          = errors.New("initial enrichment: nil context")
	errInitialEnrichmentNilActor            = errors.New("initial enrichment: nil actor")
	errInitialEnrichmentNilSource           = errors.New("initial enrichment: nil zoxide source")
	errInitialEnrichmentNilInput            = errors.New("initial enrichment: nil input stream")
	errInitialEnrichmentZeroBase            = errors.New("initial enrichment: activation generation must be nonzero")
	errInitialEnrichmentActivated           = errors.New("initial enrichment: already activated")
	errInitialEnrichmentInactive            = errors.New("initial enrichment: inactive")
	errInitialEnrichmentNilReference        = errors.New("initial enrichment: unsupported reference")
	errInitialEnrichmentCallbackApplication = errors.New("initial enrichment: callback application failed")
	errInitialEnrichmentLoadReservation     = sessionipc.ErrInvalidLoad
)

type initialZoxideLoader interface {
	LoadInitialZoxide(context.Context) (candidate.InitialZoxideResult, error)
}

type pendingEvent struct {
	generation    uint64
	closeInput    bool
	finalized     bool
	applied       bool
	loadRequested bool
}

// initialEnrichment owns the asynchronous initial zoxide source for one picker
// session. Its gate is the serialization point between source publication and
// session events that can replace the base snapshot.
type initialEnrichment struct {
	parent context.Context
	ctx    context.Context
	cancel context.CancelCauseFunc

	done  chan struct{}
	ready chan struct{}

	gate              sync.Mutex
	active            bool // zoxide may still publish an enrichment transition
	activated         bool
	initialGeneration uint64 // immutable trace identity for nonpublished source terminals
	baseGeneration    uint64
	terminalErr       error
	terminalFinalized bool
	terminal          bool // the session must not accept another event
	committing        bool
	inFlight          int
	sourceResult      candidate.InitialZoxideResult
	sourceErr         error
	traceOutcome      string
	traceGeneration   uint64
	traceCandidates   int
	discardRequested  bool
	nextEventID       uint64
	eventCancels      map[uint64]context.CancelCauseFunc
	pendingEvents     map[uint64]*pendingEvent
	stateChanged      chan struct{}

	startOnce  sync.Once
	closeOnce  sync.Once
	cancelOnce sync.Once
	traceOnce  sync.Once
	parentStop func() bool

	actor   *session.Actor
	builder initialZoxideLoader
	input   *fzf.InputStream
	metrics *pickerMetrics
	trace   *pickerTrace
	policy  candidate.ZoxidePolicy
	home    []byte
}

// newInitialEnrichment validates session-owned dependencies and starts the
// source immediately. Optional references may be pickerMetrics, pickerTrace,
// candidate.ZoxidePolicy, []byte (the session home), or
// initialEnrichmentReferences.
func newInitialEnrichment(
	parent context.Context,
	actor *session.Actor,
	source initialZoxideLoader,
	input *fzf.InputStream,
	references ...any,
) (*initialEnrichment, error) {
	if parent == nil {
		return nil, errInitialEnrichmentNilContext
	}
	if actor == nil {
		return nil, errInitialEnrichmentNilActor
	}
	if isNilInitialZoxideLoader(source) {
		return nil, errInitialEnrichmentNilSource
	}
	if input == nil {
		return nil, errInitialEnrichmentNilInput
	}

	metrics, trace, policy, home, err := parseInitialEnrichmentReferences(references)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	enrichment := &initialEnrichment{
		parent: parent, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), ready: make(chan struct{}),
		active: true, actor: actor, builder: source, input: input,
		metrics: metrics, trace: trace, policy: policy, home: append([]byte(nil), home...),
		eventCancels: make(map[uint64]context.CancelCauseFunc), pendingEvents: make(map[uint64]*pendingEvent),
		stateChanged: make(chan struct{}),
	}
	enrichment.parentStop = context.AfterFunc(parent, func() {
		enrichment.Stop(context.Cause(parent))
	})
	if err := enrichment.Start(); err != nil {
		enrichment.Stop(err)
		return nil, err
	}
	return enrichment, nil
}

type initialEnrichmentReferences struct {
	Metrics *pickerMetrics
	Trace   *pickerTrace
	Policy  candidate.ZoxidePolicy
	Home    []byte
}

func parseInitialEnrichmentReferences(references []any) (*pickerMetrics, *pickerTrace, candidate.ZoxidePolicy, []byte, error) {
	var values initialEnrichmentReferences
	for _, reference := range references {
		switch value := reference.(type) {
		case *pickerMetrics:
			values.Metrics = value
		case *pickerTrace:
			values.Trace = value
		case candidate.ZoxidePolicy:
			values.Policy = value
		case []byte:
			values.Home = append([]byte(nil), value...)
		case initialEnrichmentReferences:
			values = value
		case nil:
			// Metrics and trace are optional for focused coordinator users.
			continue
		default:
			return nil, nil, 0, nil, fmt.Errorf("%w: %T", errInitialEnrichmentNilReference, reference)
		}
	}
	if values.Policy == 0 && values.Metrics != nil {
		values.Policy = values.Metrics.policy
	}
	return values.Metrics, values.Trace, values.Policy, values.Home, nil
}

func isNilInitialZoxideLoader(loader initialZoxideLoader) bool {
	value := reflect.ValueOf(loader)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Start is idempotent. A coordinator created by newInitialEnrichment is
// already started; the method exists so lifecycle wiring can call Start at its
// ownership boundary without risking a second source goroutine.
func (enrichment *initialEnrichment) Start() error {
	if enrichment == nil {
		return errInitialEnrichmentNilSource
	}
	enrichment.startOnce.Do(func() {
		go enrichment.run()
	})
	return nil
}

func (enrichment *initialEnrichment) run() {
	defer func() {
		if enrichment.parentStop != nil {
			enrichment.parentStop()
		}
		enrichment.emitSourceTerminal()
		close(enrichment.done)
	}()

	sourceStarted := time.Now()
	result, sourceErr := enrichment.builder.LoadInitialZoxide(enrichment.ctx)
	if result.Metrics.ZoxideDuration == 0 {
		result.Metrics.ZoxideDuration = time.Since(sourceStarted)
		if result.Metrics.ZoxideDuration == 0 {
			result.Metrics.ZoxideDuration = time.Nanosecond
		}
	}
	enrichment.setSourceResult(result, sourceErr)
	if cause := context.Cause(enrichment.parent); cause != nil {
		if sourceErr != nil {
			enrichment.finishSource(joinLifecycleErrors(sourceErr, cause))
		} else {
			enrichment.Stop(cause)
		}
		return
	}
	if !enrichment.waitForActivation() {
		enrichment.retainInterruptedSourceError(sourceErr)
		return
	}
	if !enrichment.isActive() {
		enrichment.retainInterruptedSourceError(sourceErr)
		return
	}
	if cause := context.Cause(enrichment.parent); cause != nil {
		enrichment.Stop(cause)
		return
	}
	if sourceErr != nil || result.Discarded {
		// A terminal source result has nothing to commit, but it must still wait
		// for local activation so the startup stream remains usable until the
		// local generation is published. A non-nil error is authoritative.
		enrichment.finishSource(sourceErr)
		return
	}
	enrichment.commit(result)
}

func (enrichment *initialEnrichment) retainInterruptedSourceError(sourceErr error) {
	if sourceErr == nil {
		return
	}
	if errors.Is(sourceErr, context.Canceled) && context.Cause(enrichment.parent) == nil {
		return
	}
	enrichment.finishSource(sourceErr)
}

func (enrichment *initialEnrichment) waitForActivation() bool {
	select {
	case <-enrichment.ready:
		return true
	case <-enrichment.ctx.Done():
		// Navigation may cancel the enrichment owner while the source is still
		// waiting for activation. That is a discard, not a terminal session stop.
		if cause := context.Cause(enrichment.parent); cause != nil {
			enrichment.Stop(cause)
		}
		return false
	}
}

// Activate publishes the exact nonzero local generation that is eligible for
// enrichment. It can be accepted once only.
func (enrichment *initialEnrichment) Activate(baseGeneration uint64) error {
	if enrichment == nil {
		return errInitialEnrichmentNilSource
	}
	if baseGeneration == 0 {
		return errInitialEnrichmentZeroBase
	}
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	if enrichment.activated {
		return errInitialEnrichmentActivated
	}
	if !enrichment.active {
		if enrichment.terminalErr != nil {
			return enrichment.terminalErr
		}
		return errInitialEnrichmentInactive
	}
	if cause := context.Cause(enrichment.parent); cause != nil {
		return cause
	}
	enrichment.activated = true
	enrichment.initialGeneration = baseGeneration
	enrichment.baseGeneration = baseGeneration
	close(enrichment.ready)
	return nil
}

func (enrichment *initialEnrichment) isActive() bool {
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	return enrichment.active
}

func (enrichment *initialEnrichment) finishSource(sourceErr error) {
	enrichment.gate.Lock()
	cause, changed := enrichment.deactivateLocked(sourceErr)
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if changed {
		enrichment.finish(cause)
	}
}

func (enrichment *initialEnrichment) commit(source candidate.InitialZoxideResult) {
	if !enrichment.beginCommit() {
		return
	}

	enrichment.gate.Lock()
	baseGeneration := enrichment.baseGeneration
	enrichment.gate.Unlock()
	base, err := enrichment.actor.Snapshot(enrichment.ctx, baseGeneration)
	if err != nil || base.Generation() != baseGeneration {
		if err != nil && !errors.Is(err, session.ErrTransitionPending) && !errors.Is(err, context.Canceled) {
			if current, currentErr := enrichment.actor.Current(context.Background()); currentErr == nil && current.Generation() > baseGeneration {
				enrichment.setBaseGeneration(current.Generation())
			}
		}
		terminal := error(nil)
		if cause := context.Cause(enrichment.parent); cause != nil {
			terminal = cause
		}
		enrichment.finishCommit(nil, terminal)
		return
	}
	sourceRecords := append([]candidate.Record(nil), source.Records...)
	candidate.CompactHomeDisplays(sourceRecords, enrichment.home)
	merged, admitted := candidate.MergeNewRecords(base.Records(), sourceRecords)
	if len(admitted) == 0 {
		enrichment.finishCommit(nil, nil)
		return
	}

	// Actor.Enrich has published the generation before the framed bytes are
	// made visible to fzf. Append copies the frame into the session-owned
	// memory stream and cannot retry after a close.
	var result session.TransitionResult
	appendErr := enrichment.input.AppendAfter(frameCandidateRecords(admitted), func() error {
		enrichment.gate.Lock()
		active := enrichment.active && !enrichment.terminal
		enrichment.gate.Unlock()
		if !active {
			return fzf.ErrInputClosed
		}
		var err error
		result, err = enrichment.actor.Enrich(enrichment.ctx, baseGeneration, merged, source.Metrics)
		return err
	})
	if appendErr != nil {
		terminal := error(nil)
		switch {
		case errors.Is(appendErr, fzf.ErrInputClosed),
			errors.Is(appendErr, context.Canceled),
			errors.Is(appendErr, context.DeadlineExceeded):
			// Normal fzf exit and coordinator cancellation discard the
			// transaction without becoming picker errors.
		default:
			terminal = appendErr
		}
		if cause := context.Cause(enrichment.parent); cause != nil {
			terminal = joinLifecycleErrors(terminal, cause)
		}
		enrichment.finishCommit(nil, terminal)
		return
	}
	enrichment.finishCommit(&result, nil)
}

func (enrichment *initialEnrichment) beginCommit() bool {
	for {
		enrichment.gate.Lock()
		if !enrichment.active || enrichment.terminal {
			enrichment.committing = false
			enrichment.signalLocked()
			enrichment.gate.Unlock()
			return false
		}
		if !enrichment.committing && enrichment.inFlight == 0 {
			enrichment.committing = true
		}
		if enrichment.inFlight == 0 {
			enrichment.gate.Unlock()
			return true
		}
		changed := enrichment.stateChanged
		enrichment.gate.Unlock()

		select {
		case <-changed:
		case <-enrichment.ctx.Done():
			cause := context.Cause(enrichment.parent)
			if cause != nil {
				_ = enrichment.Stop(cause)
				continue
			}
			// A navigation canceled this owner while an event was in flight.
			// Release the commit gate without terminalizing later events.
			enrichment.gate.Lock()
			enrichment.committing = false
			enrichment.signalLocked()
			enrichment.gate.Unlock()
			return false
		}
	}
}

func (enrichment *initialEnrichment) finishCommit(result *session.TransitionResult, terminal error) {
	if result != nil {
		enrichment.recordTransition(*result)
		enrichment.setTraceDecision("published", result.Snapshot.Generation(), len(result.Snapshot.Records()))
	} else if terminal == nil {
		enrichment.setTraceDecision("discarded", 0, 0)
	} else if context.Cause(enrichment.parent) != nil {
		enrichment.setTraceDecision("failed", 0, 0)
	} else if !errors.Is(terminal, fzf.ErrInputClosed) && !errors.Is(terminal, context.Canceled) &&
		!errors.Is(terminal, context.DeadlineExceeded) {
		enrichment.setTraceDecision("failed", 0, 0)
	} else {
		enrichment.setTraceDecision("discarded", 0, 0)
	}
	enrichment.gate.Lock()
	if result != nil {
		enrichment.baseGeneration = result.Snapshot.Generation()
	}
	cause, changed := enrichment.deactivateLocked(terminal)
	enrichment.committing = false
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if changed || cause != nil {
		enrichment.finish(cause)
	}
}

func (enrichment *initialEnrichment) recordTransition(result session.TransitionResult) {
	if enrichment.metrics != nil {
		enrichment.metrics.recordTransition(result)
	}
}

// HandleEvent reserves an event under the gate, then performs session work
// without holding it. A successful event keeps its reservation until the
// callback action is acknowledged and, for reload effects, the exact
// generation has been copied by LoadGeneration.
func (enrichment *initialEnrichment) HandleEvent(ctx context.Context, event protocol.Event) (sessionipc.EventResult, error) {
	if enrichment == nil {
		return sessionipc.EventResult{}, errInitialEnrichmentNilSource
	}
	started := time.Now()
	eventCtx, eventID, cancelEvent, reserveErr := enrichment.reserveEvent(ctx)
	if reserveErr != nil {
		return sessionipc.EventResult{}, reserveErr
	}
	result, err := session.Handle(eventCtx, enrichment.actor, event)
	if err == nil {
		result = enrichment.suppressActiveRestore(event, result)
	}
	stopCause, stopSource := enrichment.resolveEvent(event, eventID, result, err)
	if err == nil && stopCause != nil && !stopSource {
		err = stopCause
	}
	cancelEvent(nil)

	duration := time.Since(started)
	if err == nil {
		if enrichment.metrics != nil {
			enrichment.metrics.recordTransition(result)
		}
		if result.Effect.ReloadGeneration != 0 {
			enrichment.setBaseGeneration(result.Snapshot.Generation())
			state := result.Snapshot.State()
			traceTransition(enrichment.trace, enrichment.policy, result, state.Location.Path)
		}
	}
	enrichment.recordCallback(duration)
	if stopSource {
		// Navigation and invalid-view restore cancel the source immediately, but
		// do not close the stream. They need the stream for their matching load;
		// terminal actions leave closure to the fzf process exit.
		enrichment.cancelSource(stopCause)
	}
	if err != nil {
		return sessionipc.EventResult{}, err
	}
	return sessionipc.EventResult{Effect: result.Effect, EventID: eventID}, nil
}

func (enrichment *initialEnrichment) suppressActiveRestore(event protocol.Event, result session.TransitionResult) session.TransitionResult {
	if event.Opcode == protocol.OpRestoreView {
		return result
	}
	if result.Effect.RestoreGeneration == 0 || result.Effect.ReloadGeneration != 0 || result.Effect.Accept || result.Effect.Abort {
		return result
	}
	enrichment.gate.Lock()
	active := enrichment.active
	enrichment.gate.Unlock()
	if !active {
		return result
	}
	if event.Opcode == protocol.OpRestoreView {
		result.Effect = protocol.Effect{}
	} else {
		result.Effect.RestoreGeneration = 0
	}
	return result
}

// FinalizeEvent acknowledges exactly one coordinator event action. Reload and
// restore reservations remain held until the matching load is finalized.
func (enrichment *initialEnrichment) FinalizeEvent(_ context.Context, request sessionipc.EventFinalizeRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	pending, ok := enrichment.pendingEvents[request.EventID]
	if !ok || pending.finalized {
		enrichment.gate.Unlock()
		return nil
	}
	if !request.Applied {
		enrichment.gate.Unlock()
		return enrichment.failCallbackApplication()
	}
	pending.finalized = true
	pending.applied = true
	if pending.generation == 0 {
		delete(enrichment.pendingEvents, request.EventID)
		if enrichment.inFlight > 0 {
			enrichment.inFlight--
		}
		enrichment.signalLocked()
	}
	enrichment.gate.Unlock()
	return nil
}

func (enrichment *initialEnrichment) BeginLoad(request sessionipc.LoadRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	if err := enrichment.validateLoadLocked(request); err != nil {
		enrichment.gate.Unlock()
		return err
	}
	pending := enrichment.pendingEvents[request.EventID]
	pending.loadRequested = true
	enrichment.gate.Unlock()
	return nil
}

func (enrichment *initialEnrichment) ValidateLoad(request sessionipc.LoadRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	return enrichment.validateLoadLocked(request)
}

func (enrichment *initialEnrichment) validateLoadLocked(request sessionipc.LoadRequest) error {
	pending, ok := enrichment.pendingEvents[request.EventID]
	if !ok || request.EventID == 0 || request.Generation == 0 || pending.generation != request.Generation ||
		!pending.finalized || !pending.applied || pending.loadRequested {
		return errInitialEnrichmentLoadReservation
	}
	return nil
}

func (enrichment *initialEnrichment) FinalizeLoad(_ context.Context, request sessionipc.LoadFinalizeRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	pending, ok := enrichment.pendingEvents[request.EventID]
	if !ok || !pending.finalized || !pending.applied {
		enrichment.gate.Unlock()
		return errInitialEnrichmentLoadReservation
	}
	if !request.Applied {
		enrichment.gate.Unlock()
		return enrichment.failCallbackApplication()
	}
	if !pending.loadRequested {
		enrichment.gate.Unlock()
		return errInitialEnrichmentLoadReservation
	}
	closeInput := pending.closeInput
	delete(enrichment.pendingEvents, request.EventID)
	if enrichment.inFlight > 0 {
		enrichment.inFlight--
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if closeInput {
		_ = enrichment.input.Close()
	}
	return nil
}

func (enrichment *initialEnrichment) failCallbackApplication() error {
	enrichment.gate.Lock()
	enrichment.terminal = true
	enrichment.discardRequested = true
	effective, _ := enrichment.deactivateLocked(errInitialEnrichmentCallbackApplication)
	enrichment.pendingEvents = make(map[uint64]*pendingEvent)
	enrichment.inFlight = len(enrichment.eventCancels)
	cancels := make([]context.CancelCauseFunc, 0, len(enrichment.eventCancels))
	for _, cancelEvent := range enrichment.eventCancels {
		cancels = append(cancels, cancelEvent)
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	for _, cancelEvent := range cancels {
		cancelEvent(effective)
	}
	enrichment.finish(effective)
	return effective
}

func (enrichment *initialEnrichment) reserveEvent(ctx context.Context) (context.Context, uint64, context.CancelCauseFunc, error) {
	var callerDone <-chan struct{}
	if ctx != nil {
		callerDone = ctx.Done()
	}
	for {
		enrichment.gate.Lock()
		if enrichment.terminal {
			err := enrichment.terminalErr
			if err == nil {
				err = context.Canceled
			}
			enrichment.gate.Unlock()
			return nil, 0, func(error) {}, err
		}
		if !enrichment.committing && enrichment.inFlight == 0 {
			var eventCtx context.Context
			var cancelEvent context.CancelCauseFunc
			if ctx == nil {
				cancelEvent = func(error) {}
			} else {
				eventCtx, cancelEvent = context.WithCancelCause(ctx)
			}
			if cancelEvent == nil {
				cancelEvent = func(error) {}
			}
			if enrichment.nextEventID == math.MaxUint64 {
				enrichment.gate.Unlock()
				return nil, 0, func(error) {}, errors.New("initial enrichment: event ID limit reached")
			}
			enrichment.nextEventID++
			eventID := enrichment.nextEventID
			enrichment.eventCancels[eventID] = cancelEvent
			enrichment.inFlight++
			enrichment.gate.Unlock()
			return eventCtx, eventID, cancelEvent, nil
		}
		changed := enrichment.stateChanged
		enrichment.gate.Unlock()
		select {
		case <-changed:
		case <-callerDone:
			return nil, 0, func(error) {}, context.Cause(ctx)
		}
	}
}

func (enrichment *initialEnrichment) resolveEvent(event protocol.Event, eventID uint64, result session.TransitionResult, err error) (error, bool) {
	enrichment.gate.Lock()
	_, active := enrichment.eventCancels[eventID]
	delete(enrichment.eventCancels, eventID)
	if !active {
		enrichment.gate.Unlock()
		return nil, false
	}
	if err != nil || enrichment.terminal {
		terminalErr := enrichment.terminalErr
		if enrichment.inFlight > 0 {
			enrichment.inFlight--
		}
		enrichment.signalLocked()
		enrichment.gate.Unlock()
		if err == nil {
			return terminalErr, false
		}
		return nil, false
	}
	generation := result.Effect.ReloadGeneration
	if generation == 0 {
		generation = result.Effect.RestoreGeneration
	}
	restore := event.Opcode == protocol.OpRestoreView && result.Effect.RestoreGeneration != 0
	enrichment.pendingEvents[eventID] = &pendingEvent{generation: generation,
		closeInput: result.Effect.ReloadGeneration != 0 || restore}
	var stopCause error
	stopSource := false
	terminalEvent := err == nil && (result.Effect.Accept || result.Effect.Abort)
	navigation := err == nil && result.Effect.ReloadGeneration != 0
	restoreDiscard := err == nil && restore && enrichment.active
	var otherEvents []context.CancelCauseFunc
	if terminalEvent {
		enrichment.terminal = true
		enrichment.discardRequested = true
		stopCause, _ = enrichment.deactivateLocked(nil)
		stopSource = true
		otherEvents = make([]context.CancelCauseFunc, 0, len(enrichment.eventCancels))
		for _, cancelEvent := range enrichment.eventCancels {
			otherEvents = append(otherEvents, cancelEvent)
		}
	} else if restoreDiscard {
		enrichment.discardRequested = true
		stopCause, stopSource = enrichment.deactivateLocked(nil)
	} else if navigation && enrichment.active {
		if result.Snapshot.Generation() != 0 {
			enrichment.baseGeneration = result.Snapshot.Generation()
		}
		enrichment.discardRequested = true
		stopCause, stopSource = enrichment.deactivateLocked(nil)
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if terminalEvent {
		cancelCause := stopCause
		if cancelCause == nil {
			cancelCause = context.Canceled
		}
		for _, cancelEvent := range otherEvents {
			cancelEvent(cancelCause)
		}
	}
	return stopCause, stopSource
}

func (enrichment *initialEnrichment) recordCallback(duration time.Duration) {
	if enrichment.metrics != nil {
		enrichment.metrics.recordCallback(duration)
	}
}

// Stop terminalizes the coordinator, closes the stream, and cancels the
// source. It never waits for the source goroutine; callers that need a joined
// lifecycle use Wait.
func (enrichment *initialEnrichment) Stop(cause error) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	enrichment.terminal = true
	enrichment.discardRequested = context.Cause(enrichment.parent) == nil
	effective, _ := enrichment.deactivateLocked(cause)
	enrichment.pendingEvents = make(map[uint64]*pendingEvent)
	enrichment.inFlight = len(enrichment.eventCancels)
	cancels := make([]context.CancelCauseFunc, 0, len(enrichment.eventCancels))
	for _, cancelEvent := range enrichment.eventCancels {
		cancels = append(cancels, cancelEvent)
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	cancelCause := effective
	if cancelCause == nil {
		cancelCause = context.Canceled
	}
	for _, cancelEvent := range cancels {
		cancelEvent(cancelCause)
	}
	enrichment.finish(effective)
	return effective
}

// Wait joins the source goroutine and returns only the authoritative lifecycle
// error. Soft source failures are deliberately not retained here.
func (enrichment *initialEnrichment) Wait() error {
	if enrichment == nil {
		return errInitialEnrichmentNilSource
	}
	<-enrichment.done
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	if !enrichment.terminalFinalized {
		enrichment.terminalErr = joinLifecycleErrors(enrichment.terminalErr, context.Cause(enrichment.parent))
		enrichment.terminalFinalized = true
	}
	return enrichment.terminalErr
}

func (enrichment *initialEnrichment) deactivateLocked(cause error) (error, bool) {
	cause = joinLifecycleErrors(cause, context.Cause(enrichment.parent))
	if !enrichment.active {
		if cause != nil && !enrichment.terminalFinalized {
			enrichment.terminalErr = joinLifecycleErrors(enrichment.terminalErr, cause)
		}
		return enrichment.terminalErr, false
	}
	enrichment.active = false
	enrichment.terminalErr = joinLifecycleErrors(enrichment.terminalErr, cause)
	return enrichment.terminalErr, true
}

func (enrichment *initialEnrichment) signalLocked() {
	if enrichment.stateChanged == nil {
		enrichment.stateChanged = make(chan struct{})
		return
	}
	close(enrichment.stateChanged)
	enrichment.stateChanged = make(chan struct{})
}

func joinLifecycleErrors(existing, incoming error) error {
	if incoming == nil {
		return existing
	}
	if existing == nil {
		return incoming
	}
	if errors.Is(existing, incoming) {
		return existing
	}
	if errors.Is(incoming, existing) {
		return incoming
	}
	return errors.Join(existing, incoming)
}

func (enrichment *initialEnrichment) finish(cause error) {
	enrichment.closeOnce.Do(func() {
		if cause != nil {
			_ = enrichment.input.CloseWithError(cause)
		} else {
			_ = enrichment.input.Close()
		}
	})
	enrichment.cancelOnce.Do(func() { enrichment.cancel(cause) })
}

func (enrichment *initialEnrichment) cancelSource(cause error) {
	enrichment.cancelOnce.Do(func() { enrichment.cancel(cause) })
}

func (enrichment *initialEnrichment) setSourceResult(result candidate.InitialZoxideResult, sourceErr error) {
	enrichment.gate.Lock()
	enrichment.sourceResult = result
	enrichment.sourceErr = sourceErr
	enrichment.gate.Unlock()
}

func (enrichment *initialEnrichment) setTraceDecision(outcome string, generation uint64, candidates int) {
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	if enrichment.traceOutcome != "" {
		return
	}
	if outcome != "published" {
		generation = 0
		candidates = 0
	}
	enrichment.traceOutcome = outcome
	enrichment.traceGeneration = generation
	enrichment.traceCandidates = candidates
}

func (enrichment *initialEnrichment) setBaseGeneration(generation uint64) {
	if generation == 0 {
		return
	}
	enrichment.gate.Lock()
	enrichment.baseGeneration = generation
	enrichment.gate.Unlock()
}

func (enrichment *initialEnrichment) emitSourceTerminal() {
	enrichment.traceOnce.Do(func() {
		enrichment.gate.Lock()
		result := enrichment.sourceResult
		sourceErr := enrichment.sourceErr
		lifecycle := enrichment.traceOutcome
		generation := enrichment.traceGeneration
		candidateCount := enrichment.traceCandidates
		initialGeneration := enrichment.initialGeneration
		discardRequested := enrichment.discardRequested
		enrichment.gate.Unlock()

		source := normalizeSourceMetrics(result.Metrics, sourceErr, enrichment.parent)
		if lifecycle == "" {
			if context.Cause(enrichment.parent) != nil {
				lifecycle = "failed"
			} else {
				switch {
				case sourceErr != nil && source.ZoxideOutcome != "cancelled" && source.ZoxideOutcome != "timeout":
					lifecycle = "failed"
				case source.ZoxideOutcome == "missing" || source.ZoxideOutcome == "process-error" ||
					source.ZoxideOutcome == "malformed" || source.ZoxideOutcome == "timeout":
					lifecycle = "failed"
				case source.ZoxideOutcome == "cancelled" && !discardRequested:
					lifecycle = "failed"
				case result.Discarded && !discardRequested:
					lifecycle = "failed"
				case discardRequested:
					lifecycle = "discarded"
				default:
					lifecycle = "discarded"
				}
			}
		}
		if lifecycle != "published" {
			candidateCount = 0
			generation = initialGeneration
		}
		if generation == 0 {
			// The actor reserves generation one before the initial local build.
			// Keep the standalone source terminal valid even when that build never
			// reaches an actor snapshot.
			generation = 1
		}
		if enrichment.metrics != nil {
			enrichment.metrics.recordZoxideSource(source)
		}
		traceZoxideEnrichment(enrichment.trace, enrichment.policy, generation, lifecycle, candidateCount, source)
	})
}

func normalizeSourceMetrics(source candidate.SourceMetrics, sourceErr error, parent context.Context) candidate.SourceMetrics {
	parentCause := context.Cause(parent)
	if sourceErr != nil {
		switch {
		case parentCause != nil && errors.Is(sourceErr, parentCause):
			source.ZoxideOutcome = "cancelled"
		case errors.Is(sourceErr, context.DeadlineExceeded):
			source.ZoxideOutcome = "timeout"
		case errors.Is(sourceErr, context.Canceled):
			source.ZoxideOutcome = "cancelled"
		case source.ZoxideOutcome == "" || source.ZoxideOutcome == "ok" || source.ZoxideOutcome == "cached":
			source.ZoxideOutcome = "process-error"
		}
	}
	if source.ZoxideOutcome == "" {
		source.ZoxideOutcome = "process-error"
	}
	if source.ZoxideOutcome == "pending" || source.ZoxideOutcome == "not-run" {
		source.ZoxideOutcome = "process-error"
	}
	return source
}
