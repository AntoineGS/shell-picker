package app

import (
	"bytes"
	"context"
	"errors"
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
