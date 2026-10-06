package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
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
