package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
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

func TestNewInitialEnrichmentValidatesDependencies(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	stream := fzf.NewInputStream(nil)
	for _, test := range []struct {
		name   string
		parent context.Context
		actor  *session.Actor
		source initialZoxideLoader
		input  *fzf.InputStream
	}{
		{name: "nil context", actor: actor, source: initialZoxideSourceFunc(func(context.Context) (candidate.InitialZoxideResult, error) {
			return candidate.InitialZoxideResult{}, nil
		}), input: stream},
		{name: "nil actor", parent: context.Background(), source: initialZoxideSourceFunc(func(context.Context) (candidate.InitialZoxideResult, error) {
			return candidate.InitialZoxideResult{}, nil
		}), input: stream},
		{name: "nil source", parent: context.Background(), actor: actor, input: stream},
		{name: "nil stream", parent: context.Background(), actor: actor, source: initialZoxideSourceFunc(func(context.Context) (candidate.InitialZoxideResult, error) {
			return candidate.InitialZoxideResult{}, nil
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newInitialEnrichment(test.parent, test.actor, test.source, test.input); err == nil {
				t.Fatal("constructor succeeded for invalid dependency")
			}
		})
	}
}

func TestInitialEnrichmentStartsExactlyOneSourceBeforeActivation(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/zoxide"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("source calls=%d, want 1", got)
	}
	if err := enrichment.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got := currentEnrichmentSnapshot(t, actor); got.Generation() != 1 {
		t.Fatalf("actor generation before source release=%d, want 1", got.Generation())
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 2 {
		t.Fatalf("actor generation=%d, want 2", current.Generation())
	}
	assertEnrichmentPaths(t, current.Records(), "/base", "/zoxide")
	data, err := readEnrichmentStream(t, stream)
	if err != nil {
		t.Fatalf("stream read: %v", err)
	}
	if !bytes.Contains(data, []byte("zoxide")) {
		t.Fatalf("stream=%q does not contain appended zoxide record", data)
	}
}

func TestInitialEnrichmentWaitsForActivationWhenSourceCompletesFirst(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/early"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.finished, "zoxide source")
	if got := currentEnrichmentSnapshot(t, actor); got.Generation() != 1 {
		t.Fatalf("actor generation before activation=%d, want 1", got.Generation())
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	assertEnrichmentPaths(t, currentEnrichmentSnapshot(t, actor).Records(), "/base", "/early")
}

func TestInitialEnrichmentPublishesActorBeforeExposingStreamBytes(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/ordered")}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	observed := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		n, err := stream.Read(buffer)
		if err != nil {
			observed <- err
			return
		}
		raw := bytes.TrimSuffix(buffer[:n], []byte{0})
		_, err = actor.ResolveCurrent(context.Background(), raw)
		observed <- err
	}()
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("actor did not resolve stream record: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not expose the admitted record")
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestInitialEnrichmentActivationAcceptsOneNonzeroGeneration(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/one")}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	if err := enrichment.Activate(0); err == nil {
		t.Fatal("Activate(0) succeeded")
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate(1): %v", err)
	}
	if err := enrichment.Activate(2); err == nil {
		t.Fatal("second Activate succeeded")
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestInitialEnrichmentDeduplicationDoesNotPublishGeneration(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}),
		result: candidate.InitialZoxideResult{
			Records: []candidate.Record{enrichmentRecord(protocol.KindZoxide, "/base")},
			Metrics: candidate.SourceMetrics{ZoxideOutcome: "cached"},
		},
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 1 {
		t.Fatalf("generation=%d, want unchanged generation 1", current.Generation())
	}
	assertEnrichmentPaths(t, current.Records(), "/base")
	data, err := readEnrichmentStream(t, stream)
	if err != nil || len(data) != 0 {
		t.Fatalf("stream=(%q, %v), want empty normal close", data, err)
	}
}

func TestInitialEnrichmentSoftSourceFailureLeavesActorUnchanged(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}),
		result: candidate.InitialZoxideResult{Discarded: true},
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait returned soft source failure: %v", err)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 1 {
		t.Fatalf("generation=%d, want 1", current.Generation())
	}
	assertEnrichmentPaths(t, current.Records(), "/base")
	data, err := readEnrichmentStream(t, stream)
	if err != nil || len(data) != 0 {
		t.Fatalf("stream=(%q, %v), want empty normal close", data, err)
	}
}

func TestInitialEnrichmentSoftTerminalWaitsForActivation(t *testing.T) {
	tests := []struct {
		name   string
		result candidate.InitialZoxideResult
	}{
		{name: "discarded", result: candidate.InitialZoxideResult{Discarded: true}},
		{name: "soft-empty", result: candidate.InitialZoxideResult{Metrics: candidate.SourceMetrics{ZoxideOutcome: "missing"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actor := newEnrichmentActor(t, protocol.ModeInsert)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}),
				result: test.result,
			}
			stream := fzf.NewInputStream(nil)
			enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
			awaitEnrichmentChannel(t, source.finished, "soft terminal source")

			waitDone := make(chan error, 1)
			go func() { waitDone <- enrichment.Wait() }()
			select {
			case err := <-waitDone:
				t.Fatalf("Wait returned before activation: %v", err)
			default:
			}
			if err := stream.Append([]byte("local\x00")); err != nil {
				t.Fatalf("local append before activation: %v", err)
			}
			if err := enrichment.Activate(1); err != nil {
				t.Fatalf("Activate after soft terminal: %v", err)
			}
			select {
			case err := <-waitDone:
				if err != nil {
					t.Fatalf("Wait: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Wait did not finish after activation")
			}
			if got := currentEnrichmentSnapshot(t, actor); got.Generation() != 1 {
				t.Fatalf("generation=%d, want 1", got.Generation())
			}
			data, err := readEnrichmentStream(t, stream)
			if err != nil || string(data) != "local\x00" {
				t.Fatalf("stream=(%q, %v), want local bytes and normal close", data, err)
			}
		})
	}
}

func TestInitialEnrichmentDiscardsStaleActorBase(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/late"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		BaseGeneration: 1,
		State:          session.State{Picker: protocol.PickerCD, Mode: protocol.ModeNormal, Location: pathutil.Filesystem([]byte("/next"))},
		Build:          &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/next"))},
	}); err != nil {
		t.Fatalf("publish newer actor base: %v", err)
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 2 || string(current.State().Location.Path) != "/next" {
		t.Fatalf("actor current=%+v, want newer base", current)
	}
	data, err := readEnrichmentStream(t, stream)
	if err != nil || len(data) != 0 {
		t.Fatalf("stream=(%q, %v), want empty normal close", data, err)
	}
}

func TestInitialEnrichmentDiscardsPendingActorTransition(t *testing.T) {
	pendingStarted := make(chan struct{})
	releasePending := make(chan struct{})
	var calls atomic.Int32
	actor := session.New(context.Background(), func(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if calls.Add(1) > 1 {
			close(pendingStarted)
			select {
			case <-releasePending:
			case <-ctx.Done():
				return candidate.BuildResult{}, context.Cause(ctx)
			}
		}
		return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, "/base")}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/work"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work")), Initial: true},
	}); err != nil {
		t.Fatalf("initialize actor: %v", err)
	}
	pending := make(chan error, 1)
	go func() {
		_, err := actor.Apply(context.Background(), session.ProposedTransition{
			BaseGeneration: 1,
			State:          session.State{Picker: protocol.PickerCD, Mode: protocol.ModeNormal, Location: pathutil.Filesystem([]byte("/next"))},
			Build:          &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/next"))},
		})
		pending <- err
	}()
	awaitEnrichmentChannel(t, pendingStarted, "pending actor transition")

	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/pending")}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, session.ErrTransitionPending) {
		t.Fatalf("Wait: %v, want hard publication error %v", err, session.ErrTransitionPending)
	}
	if got := currentEnrichmentSnapshot(t, actor); got.Generation() != 1 {
		t.Fatalf("generation=%d, want pending transition to remain unpublished", got.Generation())
	}
	close(releasePending)
	select {
	case err := <-pending:
		if err != nil {
			t.Fatalf("pending transition: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending transition did not complete")
	}
}

func TestInitialEnrichmentModeEventRemainsActive(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/mode")}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	modeResult, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpModeAdd})
	if err != nil {
		t.Fatalf("mode event: %v", err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, modeResult)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 2 || current.State().Mode != protocol.ModeAdd {
		t.Fatalf("current=%+v, want active mode state plus enrichment", current)
	}
	assertEnrichmentPaths(t, current.Records(), "/base", "/mode")
}

func TestInitialEnrichmentNavigationWinsAndLateSourceIsDiscarded(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/late"), ignoreCtx: true,
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
	if err != nil || effect.ReloadGeneration == 0 {
		t.Fatalf("navigation effect=%+v err=%v", effect, err)
	}
	data := acknowledgeEnrichmentEvent(t, enrichment, effect)
	if !bytes.Contains(data, []byte("local")) {
		t.Fatalf("navigation bytes=%q do not contain copied navigation records", data)
	}
	data, err = readEnrichmentStream(t, stream)
	if err != nil || len(data) != 0 {
		t.Fatalf("stream=(%q, %v), want closed after navigation load", data, err)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 2 {
		t.Fatalf("generation=%d, want navigation generation 2", current.Generation())
	}
	for _, record := range current.Records() {
		if record.Kind == protocol.KindZoxide {
			t.Fatalf("late zoxide record published: %+v", record)
		}
	}
}

func TestInitialEnrichmentAllowsSequentialNavigationAfterZoxideDiscard(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/late"), ignoreCtx: true,
	}
	var releaseOnce sync.Once
	releaseSource := func() { releaseOnce.Do(func() { close(source.release) }) }
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	t.Cleanup(releaseSource)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	for index := 0; index < 3; index++ {
		effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
		if err != nil || effect.ReloadGeneration == 0 {
			t.Fatalf("navigation %d effect=%+v err=%v", index+1, effect, err)
		}
		acknowledgeEnrichmentEvent(t, enrichment, effect)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 4 {
		t.Fatalf("generation=%d, want three navigations after initial generation", current.Generation())
	}
	releaseSource()
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for _, record := range currentEnrichmentSnapshot(t, actor).Records() {
		if record.Kind == protocol.KindZoxide {
			t.Fatalf("late zoxide record published: %+v", record)
		}
	}
}

func TestInitialEnrichmentTerminalEventsAfterNaturalCompletion(t *testing.T) {
	tests := []struct {
		name  string
		mode  protocol.Mode
		event protocol.Opcode
		want  func(protocol.Effect) bool
	}{
		{name: "accept", mode: protocol.ModeInsert, event: protocol.OpEnter, want: func(effect protocol.Effect) bool { return effect.Accept }},
		{name: "abort", mode: protocol.ModeNormal, event: protocol.OpEscape, want: func(effect protocol.Effect) bool { return effect.Abort }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actor := newEnrichmentActor(t, test.mode)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/completed"),
			}
			enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
			if err := enrichment.Activate(1); err != nil {
				t.Fatalf("Activate: %v", err)
			}
			if err := awaitEnrichmentWait(t, enrichment); err != nil {
				t.Fatalf("initial Wait: %v", err)
			}
			effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: test.event})
			if err != nil || !test.want(effect.Effect) {
				t.Fatalf("terminal effect=%+v err=%v", effect, err)
			}
			acknowledgeEnrichmentEvent(t, enrichment, effect)
			if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView}); !errors.Is(err, context.Canceled) {
				t.Fatalf("event after terminal error=%v, want cancellation", err)
			}
		})
	}
}

func TestInitialEnrichmentWinsBeforeNavigationAndNavigationUsesNewBase(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/first")}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
	if err != nil || effect.ReloadGeneration == 0 {
		t.Fatalf("navigation effect=%+v err=%v", effect, err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, effect)
	current := currentEnrichmentSnapshot(t, actor)
	if current.Generation() != 3 {
		t.Fatalf("generation=%d, want enrichment then navigation generations 2 and 3", current.Generation())
	}
	for _, record := range current.Records() {
		if record.Kind == protocol.KindZoxide {
			t.Fatalf("navigation retained zoxide record: %+v", record)
		}
	}
}

func TestInitialEnrichmentAcceptAndAbortCloseBeforeReturningAndCancelSource(t *testing.T) {
	tests := []struct {
		name  string
		mode  protocol.Mode
		event protocol.Event
	}{
		{name: "accept", mode: protocol.ModeInsert, event: protocol.Event{Opcode: protocol.OpEnter}},
		{name: "abort", mode: protocol.ModeNormal, event: protocol.Event{Opcode: protocol.OpEscape}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actor := newEnrichmentActor(t, test.mode)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
				canceled: make(chan struct{}), result: enrichmentSource("/ignored"),
			}
			stream := fzf.NewInputStream(nil)
			enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
			awaitEnrichmentChannel(t, source.started, "zoxide source")
			effect, err := enrichment.HandleEvent(context.Background(), test.event)
			if err != nil || (test.name == "accept" && !effect.Accept) || (test.name == "abort" && !effect.Abort) {
				t.Fatalf("effect=%+v err=%v", effect, err)
			}
			if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: effect.EventID, Applied: true}); err != nil {
				t.Fatalf("FinalizeEvent: %v", err)
			}
			readDone := make(chan error, 1)
			go func() {
				var buffer [1]byte
				_, readErr := stream.Read(buffer[:])
				readDone <- readErr
			}()
			select {
			case readErr := <-readDone:
				t.Fatalf("terminal finalize closed input before fzf exit: %v", readErr)
			default:
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("simulated fzf exit: %v", err)
			}
			if readErr := <-readDone; !errors.Is(readErr, io.EOF) {
				t.Fatalf("post-exit input read error=%v, want EOF", readErr)
			}
			awaitEnrichmentChannel(t, source.canceled, "zoxide cancellation")
			close(source.release)
			if err := awaitEnrichmentWait(t, enrichment); err != nil {
				t.Fatalf("Wait: %v", err)
			}
		})
	}
}

func TestInitialEnrichmentTerminalEventLeavesInputOpenUntilFZFExit(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		canceled: make(chan struct{}), result: enrichmentSource("/ignored"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")

	effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpEscape})
	if err != nil || !effect.Abort {
		t.Fatalf("effect=%+v err=%v", effect, err)
	}

	readDone := make(chan error, 1)
	go func() {
		var buffer [1]byte
		_, readErr := stream.Read(buffer[:])
		readDone <- readErr
	}()
	select {
	case readErr := <-readDone:
		t.Fatalf("input closed before callback response: %v", readErr)
	case <-time.After(25 * time.Millisecond):
	}

	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: effect.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	select {
	case readErr := <-readDone:
		t.Fatalf("finalize closed input before fzf exit: %v", readErr)
	default:
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("simulated fzf exit: %v", err)
	}
	select {
	case readErr := <-readDone:
		if !errors.Is(readErr, io.EOF) {
			t.Fatalf("post-exit input read error=%v, want EOF", readErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-exit input did not close")
	}
	awaitEnrichmentChannel(t, source.canceled, "zoxide cancellation")
	close(source.release)
}

func TestInitialEnrichmentStopAndWaitAreIdempotent(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		canceled: make(chan struct{}), result: enrichmentSource("/stopped"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	enrichment.Stop(nil)
	enrichment.Stop(nil)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait after normal Stop: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("second Wait after normal Stop: %v", err)
	}
	awaitEnrichmentChannel(t, source.canceled, "zoxide cancellation")
	close(source.release)
}

func TestInitialEnrichmentParentCancellationIsAuthoritative(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("picker parent stopped")
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		canceled: make(chan struct{}), result: enrichmentSource("/parent"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, parent, actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	cancel(cause)
	if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, cause) {
		t.Fatalf("Wait error=%v, want %v", err, cause)
	}
	data, err := readEnrichmentStream(t, stream)
	if !errors.Is(err, cause) || len(data) != 0 {
		t.Fatalf("stream=(%q, %v), want parent cause", data, err)
	}
	awaitEnrichmentChannel(t, source.canceled, "zoxide cancellation")
	close(source.release)
}

func TestInitialEnrichmentSoftTerminalParentCancellationOrdering(t *testing.T) {
	t.Run("parent cancellation before terminal finalization", func(t *testing.T) {
		parent, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("parent won before soft terminal")
		actor := newEnrichmentActor(t, protocol.ModeInsert)
		source := &controlledInitialZoxideSource{
			started: make(chan struct{}), finished: make(chan struct{}),
			result: candidate.InitialZoxideResult{Discarded: true},
		}
		stream := fzf.NewInputStream(nil)
		enrichment := newTestEnrichment(t, parent, actor, source, stream)
		awaitEnrichmentChannel(t, source.finished, "soft terminal source")
		cancel(cause)
		if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, cause) {
			t.Fatalf("Wait error=%v, want %v", err, cause)
		}
		data, err := readEnrichmentStream(t, stream)
		if !errors.Is(err, cause) || len(data) != 0 {
			t.Fatalf("stream=(%q, %v), want parent cause", data, err)
		}
	})

	t.Run("soft terminal finalization before parent cancellation", func(t *testing.T) {
		parent, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("parent won after soft terminal")
		actor := newEnrichmentActor(t, protocol.ModeInsert)
		source := &controlledInitialZoxideSource{
			started: make(chan struct{}), finished: make(chan struct{}),
			result: candidate.InitialZoxideResult{Discarded: true},
		}
		stream := fzf.NewInputStream(nil)
		enrichment := newTestEnrichment(t, parent, actor, source, stream)
		awaitEnrichmentChannel(t, source.finished, "soft terminal source")
		if err := enrichment.Activate(1); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		data, err := readEnrichmentStream(t, stream)
		if err != nil || len(data) != 0 {
			t.Fatalf("stream=(%q, %v), want normal soft close", data, err)
		}
		cancel(cause)
		if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, cause) {
			t.Fatalf("Wait error=%v, want %v", err, cause)
		}
		if got := currentEnrichmentSnapshot(t, actor); got.Generation() != 1 {
			t.Fatalf("generation=%d, want unchanged generation 1", got.Generation())
		}
	})
}

func TestInitialEnrichmentRetainsHardSourceError(t *testing.T) {
	sourceErr := errors.New("fresh zoxide cache creation failed")
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), err: sourceErr,
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.finished, "hard source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, sourceErr) {
		t.Fatalf("Wait error=%v, want hard source error %v", err, sourceErr)
	}
	data, err := readEnrichmentStream(t, stream)
	if !errors.Is(err, sourceErr) || len(data) != 0 {
		t.Fatalf("stream=(%q, %v), want hard source error", data, err)
	}
}

func TestInitialEnrichmentRetainsFreshCacheCreationFailure(t *testing.T) {
	factoryErr := errors.New("fresh cache factory failed")
	builder := new(candidate.Builder)
	builder.ConfigureFresh(func() (*candidate.ZoxideCache, error) { return nil, factoryErr })
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, builder, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, factoryErr) {
		t.Fatalf("Wait error=%v, want factory error %v", err, factoryErr)
	}
}

func TestInitialEnrichmentJoinsHardSourceErrorWithParentCause(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	parentErr := errors.New("picker stopped")
	sourceErr := errors.New("zoxide waiter failed")
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), err: sourceErr,
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, parent, actor, source, stream)
	awaitEnrichmentChannel(t, source.finished, "hard source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	data, err := readEnrichmentStream(t, stream)
	if !errors.Is(err, sourceErr) {
		t.Fatalf("stream error=%v, want source error", err)
	}
	cancel(parentErr)
	waitErr := awaitEnrichmentWait(t, enrichment)
	if !errors.Is(waitErr, sourceErr) || !errors.Is(waitErr, parentErr) || len(data) != 0 {
		t.Fatalf("Wait=%v stream=(%q,%v), want joined source and parent errors", waitErr, data, err)
	}
}

func TestInitialEnrichmentRetainsHardSourceErrorWhenParentCancelsBeforeResult(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	parentErr := errors.New("picker stopped before zoxide returned")
	sourceErr := errors.New("zoxide waiter cancellation")
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/ignored"), err: sourceErr, ignoreCtx: true,
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, parent, actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	cancel(parentErr)
	close(source.release)
	waitErr := awaitEnrichmentWait(t, enrichment)
	if !errors.Is(waitErr, sourceErr) || !errors.Is(waitErr, parentErr) {
		t.Fatalf("Wait=%v, want source and parent causes", waitErr)
	}
}

func TestInitialEnrichmentRetainsHardSourceErrorWhenParentCancelsAfterResult(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	parentErr := errors.New("picker stopped after zoxide result")
	sourceErr := errors.New("zoxide result failed before activation")
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), err: sourceErr,
	}
	enrichment := newTestEnrichment(t, parent, actor, source, fzf.NewInputStream(nil))
	awaitEnrichmentChannel(t, source.finished, "hard source")
	cancel(parentErr)
	waitErr := awaitEnrichmentWait(t, enrichment)
	if !errors.Is(waitErr, sourceErr) || !errors.Is(waitErr, parentErr) {
		t.Fatalf("Wait=%v, want source and parent causes", waitErr)
	}
}

func TestInitialEnrichmentRetainsHardSourceErrorAfterActivatedNavigation(t *testing.T) {
	const runs = 64
	sourceErr := errors.New("zoxide failed after navigation discarded enrichment")
	for range runs {
		actor := newEnrichmentActor(t, protocol.ModeInsert)
		source := &controlledInitialZoxideSource{
			started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
			result: enrichmentSource("/late"), err: sourceErr, ignoreCtx: true,
		}
		enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
		awaitEnrichmentChannel(t, source.started, "zoxide source")
		if err := enrichment.Activate(1); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent}); err != nil {
			t.Fatalf("navigation: %v", err)
		}
		close(source.release)
		if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, sourceErr) {
			t.Fatalf("Wait=%v, want hard source error %v", err, sourceErr)
		}
	}
}

func TestInitialEnrichmentExternalStreamClosePreventsActorPublication(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/closed")}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait error=%v, want normal closure", err)
	}
	if got := currentEnrichmentSnapshot(t, actor); got.Generation() != 1 {
		t.Fatalf("generation=%d, want actor unchanged", got.Generation())
	}
}

func TestPickerBackendUsesInitialEnrichmentEventGate(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}), result: enrichmentSource("/backend")}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	backend := &pickerBackend{actor: actor, enrichment: enrichment}
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	effect, err := backend.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpEscape})
	if err != nil {
		t.Fatalf("backend event: %v", err)
	}
	if err := backend.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: effect.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		var buffer [1]byte
		_, readErr := stream.Read(buffer[:])
		readDone <- readErr
	}()
	select {
	case readErr := <-readDone:
		t.Fatalf("terminal finalize closed input before fzf exit: %v", readErr)
	default:
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("simulated fzf exit: %v", err)
	}
	if readErr := <-readDone; !errors.Is(readErr, io.EOF) {
		t.Fatalf("post-exit input read error=%v, want EOF", readErr)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestInitialEnrichmentStopDoesNotWaitForBlockedEvent(t *testing.T) {
	actor, navigationStarted, releaseNavigation := newBlockingNavigationActor(t)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/ignored")}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	eventDone := make(chan error, 1)
	go func() {
		_, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
		eventDone <- err
	}()
	awaitEnrichmentChannel(t, navigationStarted, "blocked navigation")

	stopDone := make(chan struct{})
	go func() {
		enrichment.Stop(nil)
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop waited for blocked event")
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait after Stop: %v", err)
	}
	releaseNavigation()
	select {
	case <-eventDone:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked event did not unwind after release")
	}
}

func TestInitialEnrichmentRejectsEventsAfterTerminalEvent(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}), ignoreCtx: true,
		result: enrichmentSource("/terminal"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpEscape}); err != nil {
		t.Fatalf("terminal event: %v", err)
	}
	if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpModeAdd}); !errors.Is(err, context.Canceled) {
		t.Fatalf("event after terminal event error=%v, want cancellation", err)
	}
	close(source.release)
}

func TestInitialEnrichmentConcurrentStopAndCommitRace(t *testing.T) {
	const runs = 20
	errorsSeen := make(chan error, runs)
	var done sync.WaitGroup
	for range runs {
		done.Add(1)
		go func() {
			defer done.Done()
			actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
				return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, "/base")}}, nil
			})
			defer actor.Close()
			if _, err := actor.Apply(context.Background(), session.ProposedTransition{
				State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/work"))},
				Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work")), Initial: true},
			}); err != nil {
				errorsSeen <- err
				return
			}
			source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}), result: enrichmentSource("/race"), ignoreCtx: true}
			enrichment, err := newInitialEnrichment(context.Background(), actor, source, fzf.NewInputStream(nil))
			if err != nil {
				errorsSeen <- err
				return
			}
			<-source.started
			if err := enrichment.Activate(1); err != nil {
				errorsSeen <- err
				return
			}
			var events sync.WaitGroup
			events.Add(2)
			go func() { defer events.Done(); enrichment.Stop(nil) }()
			go func() { defer events.Done(); close(source.release) }()
			events.Wait()
			if err := enrichment.Wait(); err != nil && !errors.Is(err, fzf.ErrInputClosed) {
				errorsSeen <- err
			}
		}()
	}
	done.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent run: %v", err)
	}
}

func newTracedEnrichment(t *testing.T, parent context.Context, actor *session.Actor, source initialZoxideLoader, stream *fzf.InputStream, metrics *pickerMetrics) (*initialEnrichment, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	trace := &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1, 2, 3})}
	enrichment, err := newInitialEnrichment(parent, actor, source, stream, metrics, trace, candidate.ZoxideCached)
	if err != nil {
		t.Fatalf("newInitialEnrichment: %v", err)
	}
	t.Cleanup(func() {
		enrichment.Stop(nil)
		_ = enrichment.Wait()
	})
	return enrichment, &output
}

func tracedEnrichmentRecords(t *testing.T, output *bytes.Buffer) []integrationpkg.TraceRecord {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	records := make([]integrationpkg.TraceRecord, 0, len(lines))
	for _, line := range lines {
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode trace line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestInitialEnrichmentEmitsOneStandalonePublishedTerminal(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/published"),
	}
	metrics := &pickerMetrics{sources: candidate.SourceMetrics{ZoxideOutcome: "not-run"}}
	stream := fzf.NewInputStream(nil)
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, stream, metrics)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	records := tracedEnrichmentRecords(t, output)
	var terminals []integrationpkg.TraceRecord
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminals = append(terminals, record)
		}
		if record.Event == "generation.publish" {
			t.Fatalf("Actor.Enrich emitted normal generation terminal: %+v", record)
		}
	}
	if len(terminals) != 1 {
		t.Fatalf("zoxide terminals=%d records=%+v", len(terminals), records)
	}
	terminal := terminals[0]
	if terminal.Outcome != "published" || terminal.Generation != 2 || terminal.CandidateCount != 2 ||
		terminal.ZoxidePolicy != "cached" || terminal.ZoxideOutcome != "cached" || terminal.ZoxideAttempts != 1 {
		t.Fatalf("terminal=%+v", terminal)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.sources.ZoxideAttempts != 1 || metrics.sources.ZoxideDuration <= 0 || metrics.sources.ZoxideOutcome != "cached" {
		t.Fatalf("metrics=%+v", metrics.sources)
	}
}

func TestInitialEnrichmentEmitsDiscardedTerminalForAllDuplicateResult(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}),
		result: candidate.InitialZoxideResult{
			Records: []candidate.Record{enrichmentRecord(protocol.KindZoxide, "/base")},
			Metrics: candidate.SourceMetrics{ZoxideOutcome: "ok", ZoxideAttempts: 1},
		},
	}
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil), &pickerMetrics{})
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	records := tracedEnrichmentRecords(t, output)
	var terminals []integrationpkg.TraceRecord
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminals = append(terminals, record)
		}
	}
	if len(terminals) != 1 || terminals[0].Outcome != "discarded" || terminals[0].Generation != 1 || terminals[0].CandidateCount != 0 || terminals[0].ZoxideOutcome != "ok" {
		t.Fatalf("terminals=%+v records=%+v", terminals, records)
	}
}

func TestInitialEnrichmentEmitsFailedTerminalForEachSoftSourceOutcome(t *testing.T) {
	for _, outcome := range []string{"missing", "process-error", "malformed", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			actor := newEnrichmentActor(t, protocol.ModeInsert)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}),
				result: candidate.InitialZoxideResult{Discarded: true, Metrics: candidate.SourceMetrics{ZoxideOutcome: outcome, ZoxideAttempts: 1}},
			}
			stream := fzf.NewInputStream(nil)
			enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, stream, &pickerMetrics{})
			if err := enrichment.Activate(1); err != nil {
				t.Fatalf("Activate: %v", err)
			}
			if err := awaitEnrichmentWait(t, enrichment); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			records := tracedEnrichmentRecords(t, output)
			var terminal integrationpkg.TraceRecord
			count := 0
			for _, record := range records {
				if record.Event == "zoxide.enrichment" {
					terminal = record
					count++
				}
			}
			if count != 1 || terminal.Outcome != "failed" || terminal.Generation != 1 || terminal.CandidateCount != 0 || terminal.ZoxideOutcome != outcome {
				t.Fatalf("count=%d terminal=%+v records=%+v", count, terminal, records)
			}
		})
	}
}

func TestInitialEnrichmentNavigationAndInputCloseDiscardOneTerminal(t *testing.T) {
	tests := []struct {
		name       string
		inputClose bool
		navigate   bool
		terminal   bool
		mode       protocol.Mode
	}{
		{name: "navigation", navigate: true, mode: protocol.ModeInsert},
		{name: "input-close", inputClose: true, mode: protocol.ModeInsert},
		{name: "terminal", terminal: true, mode: protocol.ModeNormal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actor := newEnrichmentActor(t, test.mode)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
				result: enrichmentSource("/discarded"), ignoreCtx: true,
			}
			stream := fzf.NewInputStream(nil)
			enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, stream, &pickerMetrics{})
			awaitEnrichmentChannel(t, source.started, "zoxide source")
			if test.inputClose {
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if err := enrichment.Activate(1); err != nil {
					t.Fatalf("Activate: %v", err)
				}
			} else if test.terminal {
				if err := enrichment.Activate(1); err != nil {
					t.Fatalf("Activate: %v", err)
				}
				if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpEscape}); err != nil {
					t.Fatalf("terminal: %v", err)
				}
			} else {
				if err := enrichment.Activate(1); err != nil {
					t.Fatalf("Activate: %v", err)
				}
				if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent}); err != nil {
					t.Fatalf("navigation: %v", err)
				}
			}
			close(source.release)
			if err := awaitEnrichmentWait(t, enrichment); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			records := tracedEnrichmentRecords(t, output)
			count := 0
			for _, record := range records {
				if record.Event == "zoxide.enrichment" {
					if record.Outcome != "discarded" || record.CandidateCount != 0 || record.ZoxideOutcome != "cached" {
						t.Fatalf("discard terminal=%+v", record)
					}
					count++
				}
			}
			if count != 1 {
				t.Fatalf("terminal count=%d records=%+v", count, records)
			}
		})
	}
}

func TestInitialEnrichmentEmitsDiscardedTerminalForStaleBase(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/stale"), ignoreCtx: true,
	}
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil), &pickerMetrics{})
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		BaseGeneration: 1,
		State:          session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/next"))},
		Build:          &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/next"))},
	}); err != nil {
		t.Fatalf("publish newer actor base: %v", err)
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	records := tracedEnrichmentRecords(t, output)
	var terminal integrationpkg.TraceRecord
	count := 0
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminal = record
			count++
		}
	}
	if count != 1 || terminal.Outcome != "discarded" || terminal.Generation != 1 || terminal.CandidateCount != 0 || terminal.ZoxideOutcome != "cached" {
		t.Fatalf("count=%d terminal=%+v records=%+v", count, terminal, records)
	}
}

func TestInitialEnrichmentMultipleNavigationDiscardUsesInitialGeneration(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/late"), ignoreCtx: true,
	}
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil), &pickerMetrics{})
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	for navigation := 0; navigation < 3; navigation++ {
		effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
		if err != nil || effect.ReloadGeneration != uint64(navigation+2) {
			t.Fatalf("navigation %d effect=%+v err=%v", navigation+1, effect, err)
		}
		acknowledgeEnrichmentEvent(t, enrichment, effect)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	records := tracedEnrichmentRecords(t, output)
	var terminal integrationpkg.TraceRecord
	count := 0
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminal = record
			count++
		}
	}
	if count != 1 || terminal.Outcome != "discarded" || terminal.Generation != 1 || terminal.CandidateCount != 0 || terminal.ZoxideOutcome != "cached" {
		t.Fatalf("count=%d terminal=%+v records=%+v", count, terminal, records)
	}
}

func TestInitialEnrichmentParentCancellationAndHardSourceFailureEmitFailedTerminal(t *testing.T) {
	tests := []struct {
		name       string
		parent     bool
		sourceErr  error
		wantSource string
	}{
		{name: "parent-cancel", parent: true, wantSource: "cancelled"},
		{name: "hard-source", sourceErr: errors.New("source failed"), wantSource: "process-error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := context.Background()
			cancel := context.CancelCauseFunc(func(error) {})
			if test.parent {
				parent, cancel = context.WithCancelCause(parent)
			}
			defer cancel(nil)
			actor := newEnrichmentActor(t, protocol.ModeInsert)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
				result: candidate.InitialZoxideResult{Discarded: true, Metrics: candidate.SourceMetrics{
					ZoxideOutcome: test.wantSource, ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1,
					ZoxideProcesses: 1, ZoxideMaxLive: 1,
				}}, err: test.sourceErr,
				returnResultOnCancel: test.parent,
			}
			stream := fzf.NewInputStream(nil)
			enrichment, output := newTracedEnrichment(t, parent, actor, source, stream, &pickerMetrics{})
			awaitEnrichmentChannel(t, source.started, "zoxide source")
			if test.parent {
				cancel(errors.New("parent stopped"))
			}
			close(source.release)
			_ = enrichment.Activate(1)
			_ = awaitEnrichmentWait(t, enrichment)
			records := tracedEnrichmentRecords(t, output)
			count := 0
			for _, record := range records {
				if record.Event == "zoxide.enrichment" {
					if record.Outcome != "failed" || record.ZoxideOutcome != test.wantSource || record.Generation == 0 ||
						record.ZoxideAttempts != 1 || record.ZoxideStarts != 1 || record.ZoxideExits != 1 ||
						record.ZoxideProcesses != 1 || record.ZoxideLive != 0 || record.ZoxideMaxLive != 1 {
						t.Fatalf("failed terminal=%+v", record)
					}
					count++
				}
			}
			if count != 1 {
				t.Fatalf("terminal count=%d records=%+v", count, records)
			}
		})
	}
}
