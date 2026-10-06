package app

import (
	"context"
	"errors"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

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
