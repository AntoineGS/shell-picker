package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestCompletionFastPathEditPreservesPendingZoxide(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	base, home := t.TempDir(), t.TempDir()
	current := currentEnrichmentSnapshot(t, actor)
	state := current.State()
	state.Location = pathutil.Filesystem([]byte(base))
	state.Home = pathutil.Filesystem([]byte(home))
	state.Completion = &session.CompletionContext{Base: base}
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		BaseGeneration: current.Generation(), State: state,
	}); err != nil {
		t.Fatal(err)
	}
	frequent := filepath.Join(t.TempDir(), "frequent")
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource(frequent),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(current.Generation()); err != nil {
		t.Fatal(err)
	}

	// Ordinary filename edits must remain responsive while the source is held.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := enrichment.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpQuery, Query: []byte("fre")})
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, result)

	// Begin navigation before the source can return. A correct implementation
	// waits for the pending launch suggestions before saving the launch view.
	release := time.AfterFunc(25*time.Millisecond, func() { close(source.release) })
	defer release.Stop()
	result, err = enrichment.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpQuery, Query: []byte("~")})
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, result)
	result, err = enrichment.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpQuery})
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, result)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatal(err)
	}
	current = currentEnrichmentSnapshot(t, actor)
	if string(current.State().Location.Path) != base {
		t.Fatalf("returned to %q, want %q", current.State().Location.Path, base)
	}
	assertEnrichmentPaths(t, current.Records(), "/base", frequent)
	if source.calls.Load() != 1 {
		t.Fatalf("zoxide launches=%d, want one", source.calls.Load())
	}
}

func TestCompletionPendingZoxideWaitCanBeCanceled(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	current := currentEnrichmentSnapshot(t, actor)
	state := current.State()
	state.Completion = &session.CompletionContext{Base: string(state.Location.Path)}
	state.Home = pathutil.Filesystem([]byte(t.TempDir()))
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		BaseGeneration: current.Generation(), State: state,
	}); err != nil {
		t.Fatal(err)
	}
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/frequent"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(current.Generation()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := enrichment.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpQuery, Query: []byte("~")}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled navigation: %v", err)
	}
	if got := currentEnrichmentSnapshot(t, actor); got.Generation() != current.Generation() {
		t.Fatal("canceled wait changed the current view")
	}
	// An interrupted wait must not leak a reservation or prevent shutdown.
	ctx, cancelAbort := context.WithTimeout(context.Background(), time.Second)
	defer cancelAbort()
	result, err := enrichment.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpEscape})
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, result)
	result, err = enrichment.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpEscape})
	if err != nil || !result.Abort {
		t.Fatalf("abort after canceled wait: %+v %v", result, err)
	}
	acknowledgeEnrichmentEvent(t, enrichment, result)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatal(err)
	}
}
