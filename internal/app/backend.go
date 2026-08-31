package app

import (
	"context"
	"os"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type pickerBackend struct {
	actor      *session.Actor
	metrics    *pickerMetrics
	trace      *pickerTrace
	enrichment *initialEnrichment
	stat       func(string) (os.FileInfo, error)
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
		return backend.enrichment.HandleEvent(ctx, event)
	}
	if cause := context.Cause(ctx); cause != nil {
		return sessionipc.EventResult{}, cause
	}
	transition, handleErr := session.Handle(ctx, backend.actor, event)
	duration := time.Since(started)
	if handleErr == nil {
		backend.metrics.recordTransition(transition)
		if transition.Effect.ReloadGeneration != 0 {
			state := transition.Snapshot.State()
			traceTransition(backend.trace, backend.metrics.policy, transition, state.Location.Path)
		}
	}
	backend.metrics.recordCallback(duration)
	return sessionipc.EventResult{Effect: transition.Effect}, handleErr
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
	return pathutil.PromptDisplayHome(state.Location, state.Home), nil
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
