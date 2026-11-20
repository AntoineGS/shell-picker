package app

import (
	"context"
	"os"
	"sync"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type pickerBackend struct {
	actor         *session.Actor
	metrics       *pickerMetrics
	trace         *pickerTrace
	enrichment    *initialEnrichment
	indexed       *indexedSource
	stat          func(string) (os.FileInfo, error)
	commandHeader string
	initialGate   *initialStreamGate
}

type initialStreamGate struct {
	mu         sync.Mutex
	actorReady bool
	fzfLoaded  bool
}

func (gate *initialStreamGate) markActorReady() {
	gate.mu.Lock()
	gate.actorReady = true
	gate.mu.Unlock()
}

func (gate *initialStreamGate) markFZFLoaded() {
	gate.mu.Lock()
	gate.fzfLoaded = true
	gate.mu.Unlock()
}

func (gate *initialStreamGate) ready() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.actorReady && gate.fzfLoaded
}

func (gate *initialStreamGate) published() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.actorReady
}

func (backend *pickerBackend) HandleEvent(ctx context.Context, event protocol.Event) (result sessionipc.EventResult, err error) {
	started := time.Now()
	backend.trace.event(integrationpkg.TraceEvent{Name: "callback.event.start", Outcome: "started", Timestamp: time.Now()})
	defer func() {
		outcome := string(event.Opcode)
		if err != nil {
			outcome = "error"
		}
		backend.trace.event(integrationpkg.TraceEvent{Name: "callback.event", Outcome: outcome, CallbackIPC: time.Since(started), Timestamp: time.Now()})
	}()
	if backend.enrichment != nil {
		result, handleErr := backend.enrichment.HandleEvent(ctx, event)
		if handleErr == nil && result.Effect.Header != "" {
			state, stateErr := backend.actor.CurrentState(ctx)
			if stateErr == nil {
				backend.decorateHeader(&result.Effect, state)
			}
		}
		return result, handleErr
	}
	if event.Opcode == protocol.OpInputLoaded {
		if backend.initialGate == nil || !backend.initialGate.published() {
			return sessionipc.EventResult{}, session.ErrInvalidEvent
		}
		if backend.initialGate.ready() {
			return sessionipc.EventResult{Effect: protocol.Effect{Ignore: true}}, nil
		}
		state, err := backend.syncInitialQuery(ctx, event.Query)
		if err != nil {
			return sessionipc.EventResult{}, err
		}
		backend.initialGate.markFZFLoaded()
		return sessionipc.EventResult{Effect: protocol.Effect{
			Header: pickerHeader(backend.commandHeader, state, backend.indexed),
		}}, nil
	}
	if gate := backend.initialGate; gate != nil && !gate.ready() {
		if event.Opcode == protocol.OpEscape {
			return sessionipc.EventResult{Effect: protocol.Effect{Abort: true}}, nil
		}
		return sessionipc.EventResult{Effect: protocol.Effect{Ignore: true}}, nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return sessionipc.EventResult{}, cause
	}
	transition, handleErr := session.Handle(ctx, backend.actor, event)
	duration := time.Since(started)
	if handleErr == nil {
		backend.metrics.recordTransition(transition)
		state := transition.Snapshot.State()
		backend.decorateHeader(&transition.Effect, state)
		if transition.Effect.ReloadGeneration != 0 {
			traceTransition(backend.trace, backend.metrics.policy, transition, state.Location.Path)
		}
	}
	backend.metrics.recordCallback(duration)
	return sessionipc.EventResult{Effect: transition.Effect}, handleErr
}

func (backend *pickerBackend) syncInitialQuery(ctx context.Context, query []byte) (session.State, error) {
	snapshot, err := backend.actor.Current(ctx)
	if err != nil {
		return session.State{}, err
	}
	state := snapshot.State()
	state.Query = append([]byte(nil), query...)
	if state.Source == protocol.SourceIndexed && state.Indexed != nil {
		state.Indexed.Query = append([]byte(nil), query...)
	}
	_, err = backend.actor.Apply(ctx, session.ProposedTransition{
		BaseGeneration: snapshot.Generation(), State: state,
	})
	return state, err
}

func (backend *pickerBackend) FinalizeEvent(ctx context.Context, request sessionipc.EventFinalizeRequest) error {
	if backend.enrichment != nil {
		return backend.enrichment.FinalizeEvent(ctx, request)
	}
	return nil
}

func (backend *pickerBackend) FinalizeLoad(ctx context.Context, request sessionipc.LoadFinalizeRequest) error {
	if backend.enrichment != nil {
		return backend.enrichment.FinalizeLoad(ctx, request)
	}
	return nil
}

func (backend *pickerBackend) LoadGeneration(ctx context.Context, request sessionipc.LoadRequest) (data []byte, err error) {
	started := time.Now()
	backend.trace.event(integrationpkg.TraceEvent{Name: "callback.load.start", Generation: request.Generation, Outcome: "started", Timestamp: time.Now()})
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		backend.trace.event(integrationpkg.TraceEvent{Name: "callback.load", Generation: request.Generation, Outcome: outcome, LoadDuration: time.Since(started), Timestamp: time.Now()})
	}()
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if backend.enrichment != nil {
		if err := backend.enrichment.ValidateLoad(ctx, request); err != nil {
			return nil, err
		}
	}
	snapshot, err := backend.actor.Snapshot(ctx, request.Generation)
	duration := time.Since(started)
	if err != nil {
		return nil, err
	}
	data = snapshot.FramedRecords()
	if backend.enrichment != nil {
		// Mark the exact reservation only after Snapshot and framing have copied
		// the bytes returned to fzf. FinalizeLoad performs the release.
		if err := backend.enrichment.BeginLoad(request); err != nil {
			return nil, err
		}
	}
	backend.metrics.recordLoad(duration)
	return data, nil
}

func (backend *pickerBackend) CurrentHeader(ctx context.Context) (header string, err error) {
	backend.trace.event(integrationpkg.TraceEvent{Name: "callback.display.start", Outcome: "started", Timestamp: time.Now()})
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		backend.trace.event(integrationpkg.TraceEvent{Name: "callback.display", Outcome: outcome, Timestamp: time.Now()})
	}()
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	state, err := backend.actor.CurrentState(ctx)
	if err != nil {
		return "", err
	}
	if backend.initialGate != nil && !backend.initialGate.ready() {
		return loadingIndexHeader(backend.commandHeader, state.FilesSearch()), nil
	}
	return pickerHeader(backend.commandHeader, state, backend.indexed), nil
}

func (backend *pickerBackend) ResolvePreview(ctx context.Context, current []byte) (candidate protocol.ResolvedCandidate, err error) {
	backend.trace.event(integrationpkg.TraceEvent{Name: "callback.preview.start", Outcome: "started", Timestamp: time.Now()})
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		backend.trace.event(integrationpkg.TraceEvent{Name: "callback.preview", Outcome: outcome, Timestamp: time.Now()})
	}()
	if cause := context.Cause(ctx); cause != nil {
		return protocol.ResolvedCandidate{}, cause
	}
	if backend.initialGate != nil && !backend.initialGate.ready() {
		return protocol.ResolvedCandidate{}, sessionipc.ErrNotFound
	}
	record, err := backend.actor.ResolveCurrent(ctx, current)
	if err != nil {
		return protocol.ResolvedCandidate{}, err
	}
	if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem {
		return protocol.ResolvedCandidate{}, session.ErrUnknownRecord
	}
	started := time.Now()
	stat := backend.stat
	if stat == nil {
		stat = os.Stat
	}
	info, err := stat(string(record.Path))
	if err != nil {
		return protocol.ResolvedCandidate{}, err
	}
	if cause := context.Cause(ctx); cause != nil {
		return protocol.ResolvedCandidate{}, cause
	}
	backend.metrics.recordPreviewResolve(time.Since(started))
	return protocol.ResolvedCandidate{Kind: record.Kind, Path: append([]byte(nil), record.Path...), Size: info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}, nil
}

func (backend *pickerBackend) RecordPreview(ctx context.Context, request sessionipc.PreviewRequest) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	started := time.Now()
	if err := backend.metrics.recordPreview(request); err != nil {
		return err
	}
	switch request.Phase {
	case "started":
		backend.trace.event(integrationpkg.TraceEvent{Name: "preview.dispatch", Renderer: request.Renderer, Outcome: "ok"})
	case "finished":
		backend.trace.event(integrationpkg.TraceEvent{Name: "preview.finished", Renderer: request.Renderer, Outcome: request.Outcome,
			ChildStarts: request.ChildStarts, MaxLiveChildren: request.MaxLiveChildren})
	}
	backend.metrics.recordCallback(time.Since(started))
	return context.Cause(ctx)
}
