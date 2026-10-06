package app

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type initialZoxideSourceFunc func(context.Context) (candidate.InitialZoxideResult, error)

func (source initialZoxideSourceFunc) LoadInitialZoxide(ctx context.Context) (candidate.InitialZoxideResult, error) {
	return source(ctx)
}

type controlledInitialZoxideSource struct {
	started              chan struct{}
	finished             chan struct{}
	release              chan struct{}
	canceled             chan struct{}
	result               candidate.InitialZoxideResult
	err                  error
	ignoreCtx            bool
	returnResultOnCancel bool
	calls                atomic.Int32
	startOnce            sync.Once
	finishOnce           sync.Once
	cancelOnce           sync.Once
}

func (source *controlledInitialZoxideSource) LoadInitialZoxide(ctx context.Context) (candidate.InitialZoxideResult, error) {
	source.calls.Add(1)
	source.startOnce.Do(func() { close(source.started) })
	if source.release != nil {
		if source.ignoreCtx {
			<-source.release
		} else {
			select {
			case <-source.release:
			case <-ctx.Done():
				source.cancelOnce.Do(func() {
					if source.canceled != nil {
						close(source.canceled)
					}
				})
				source.finishOnce.Do(func() { close(source.finished) })
				if source.returnResultOnCancel {
					return source.result, context.Cause(ctx)
				}
				return candidate.InitialZoxideResult{}, context.Cause(ctx)
			}
		}
	}
	if source.canceled != nil {
		select {
		case <-ctx.Done():
			source.cancelOnce.Do(func() { close(source.canceled) })
		default:
		}
	}
	source.finishOnce.Do(func() { close(source.finished) })
	return source.result, source.err
}

func enrichmentRecord(kind protocol.Kind, path string) candidate.Record {
	value := []byte(path)
	return candidate.Record{
		Kind: kind, Display: path, Path: append([]byte(nil), value...), Payload: protocol.EncodePath(value),
		Target: pathutil.Filesystem(value),
	}
}

func enrichmentSource(path string) candidate.InitialZoxideResult {
	return candidate.InitialZoxideResult{
		Records: []candidate.Record{enrichmentRecord(protocol.KindZoxide, path)},
		Metrics: candidate.SourceMetrics{ZoxideOutcome: "cached", ZoxideAttempts: 1},
	}
}

func newEnrichmentActor(t *testing.T, mode protocol.Mode) *session.Actor {
	t.Helper()
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, "/base")}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	_, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{
			Picker: protocol.PickerCD, Mode: mode, Location: pathutil.Filesystem([]byte("/work")),
			Home: pathutil.Filesystem([]byte("/work")), Prompt: "[I] ",
		},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work")), Initial: true},
	})
	if err != nil {
		t.Fatalf("initialize actor: %v", err)
	}
	return actor
}

func newBlockingNavigationActor(t *testing.T) (*session.Actor, <-chan struct{}, func()) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int32
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		if calls.Add(1) > 1 {
			close(started)
			<-release
			return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, "/next")}}, nil
		}
		return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, "/base")}}, nil
	})
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{
			Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/work")),
		},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work")), Initial: true},
	}); err != nil {
		t.Fatalf("initialize blocking actor: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = actor.Close()
	})
	return actor, started, func() { releaseOnce.Do(func() { close(release) }) }
}

func newCancellableNavigationActor(t *testing.T, mode protocol.Mode) (*session.Actor, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	var calls atomic.Int32
	actor := session.New(context.Background(), func(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if calls.Add(1) > 1 {
			startOnce.Do(func() { close(started) })
			select {
			case <-ctx.Done():
				cancelOnce.Do(func() { close(canceled) })
				return candidate.BuildResult{}, context.Cause(ctx)
			}
		}
		return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, "/base")}}, nil
	})
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{
			Picker: protocol.PickerCD, Mode: mode, Location: pathutil.Filesystem([]byte("/work")),
		},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work")), Initial: true},
	}); err != nil {
		t.Fatalf("initialize cancellable actor: %v", err)
	}
	t.Cleanup(func() { _ = actor.Close() })
	return actor, started, canceled
}

func newTestEnrichment(t *testing.T, parent context.Context, actor *session.Actor, source initialZoxideLoader, stream *fzf.InputStream) *initialEnrichment {
	t.Helper()
	enrichment, err := newInitialEnrichment(parent, actor, source, stream)
	if err != nil {
		t.Fatalf("newInitialEnrichment: %v", err)
	}
	t.Cleanup(func() {
		enrichment.Stop(nil)
		_ = enrichment.Wait()
	})
	return enrichment
}

func awaitEnrichmentChannel(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not complete", name)
	}
}

func awaitEnrichmentWait(t *testing.T, enrichment *initialEnrichment) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- enrichment.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("initial enrichment did not stop")
		return nil
	}
}

func readEnrichmentStream(t *testing.T, stream *fzf.InputStream) ([]byte, error) {
	t.Helper()
	done := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := io.ReadAll(stream)
		done <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	select {
	case result := <-done:
		return result.data, result.err
	case <-time.After(2 * time.Second):
		t.Fatal("input stream did not close")
		return nil, nil
	}
}

func currentEnrichmentSnapshot(t *testing.T, actor *session.Actor) session.Snapshot {
	t.Helper()
	snapshot, err := actor.Current(context.Background())
	if err != nil {
		t.Fatalf("actor.Current: %v", err)
	}
	return snapshot
}

func assertEnrichmentPaths(t *testing.T, records []candidate.Record, want ...string) {
	t.Helper()
	if len(records) != len(want) {
		t.Fatalf("record count=%d, want %d (%q)", len(records), len(want), want)
	}
	for index, path := range want {
		if got := string(records[index].Path); got != path {
			t.Fatalf("record[%d].Path=%q, want %q", index, got, path)
		}
	}
}

func acknowledgeEnrichmentEvent(t *testing.T, enrichment *initialEnrichment, result sessionipc.EventResult) []byte {
	t.Helper()
	if result.EventID == 0 {
		t.Fatal("coordinator event did not return a nonzero event ID")
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	generation := result.Effect.ReloadGeneration
	if generation == 0 {
		generation = result.Effect.RestoreGeneration
	}
	if generation == 0 {
		return nil
	}
	backend := &pickerBackend{actor: enrichment.actor, enrichment: enrichment, metrics: &pickerMetrics{}}
	data, err := backend.LoadGeneration(context.Background(), sessionipc.LoadRequest{Generation: generation, EventID: result.EventID})
	if err != nil {
		t.Fatalf("LoadGeneration(%d): %v", generation, err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeLoad(%d): %v", result.EventID, err)
	}
	return data
}
