package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

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
