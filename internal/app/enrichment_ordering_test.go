package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestInitialEnrichmentLoadRequiresExactEventAndGenerationAndFinalization(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		ignoreCtx: true, result: enrichmentSource("/late"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	result, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
	if err != nil || result.Effect.ReloadGeneration == 0 || result.EventID == 0 {
		t.Fatalf("navigation result=%+v err=%v", result, err)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	backend := &pickerBackend{actor: actor, enrichment: enrichment, metrics: &pickerMetrics{}}
	wrongID := sessionipc.LoadRequest{Generation: result.Effect.ReloadGeneration, EventID: result.EventID + 1}
	if _, err := backend.LoadGeneration(context.Background(), wrongID); !errors.Is(err, errInitialEnrichmentLoadReservation) {
		t.Fatalf("wrong event ID error=%v", err)
	}
	wrongGeneration := sessionipc.LoadRequest{Generation: result.Effect.ReloadGeneration + 1, EventID: result.EventID}
	if _, err := backend.LoadGeneration(context.Background(), wrongGeneration); !errors.Is(err, errInitialEnrichmentLoadReservation) {
		t.Fatalf("wrong generation error=%v", err)
	}
	request := sessionipc.LoadRequest{Generation: result.Effect.ReloadGeneration, EventID: result.EventID}
	data, err := backend.LoadGeneration(context.Background(), request)
	if err != nil || !bytes.Contains(data, []byte("base")) {
		t.Fatalf("exact load data=%q err=%v", data, err)
	}
	if _, err := backend.LoadGeneration(context.Background(), request); !errors.Is(err, errInitialEnrichmentLoadReservation) {
		t.Fatalf("replayed load error=%v", err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeLoad: %v", err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: result.EventID, Applied: true}); !errors.Is(err, errInitialEnrichmentLoadReservation) {
		t.Fatalf("replayed FinalizeLoad error=%v", err)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := enrichment.inFlight; got != 0 {
		t.Fatalf("inFlight=%d after exact load finalization", got)
	}
}

func TestInitialEnrichmentLoadAppliedFalseIsHardCallbackFailure(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/late")}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	result, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
	if err != nil {
		t.Fatalf("navigation: %v", err)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	backend := &pickerBackend{actor: actor, enrichment: enrichment, metrics: &pickerMetrics{}}
	if _, err := backend.LoadGeneration(context.Background(), sessionipc.LoadRequest{Generation: result.Effect.ReloadGeneration, EventID: result.EventID}); err != nil {
		t.Fatalf("LoadGeneration: %v", err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: result.EventID, Applied: false}); err == nil {
		t.Fatal("FinalizeLoad Applied=false returned nil")
	}
	if err := awaitEnrichmentWait(t, enrichment); err == nil || !errors.Is(err, errInitialEnrichmentCallbackApplication) {
		t.Fatalf("Wait error=%v, want callback application failure", err)
	}
}

func TestInitialEnrichmentLoadAppliedFalseBeforeBeginLoadIsHardCallbackFailure(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		canceled: make(chan struct{}), result: enrichmentSource("/blocked"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	result, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
	if err != nil {
		t.Fatalf("navigation: %v", err)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: result.EventID, Applied: false}); !errors.Is(err, errInitialEnrichmentCallbackApplication) {
		t.Fatalf("FinalizeLoad error=%v, want callback application failure", err)
	}
	awaitEnrichmentChannel(t, source.canceled, "zoxide cancellation")
	awaitEnrichmentChannel(t, source.finished, "zoxide reap")
	if err := awaitEnrichmentWait(t, enrichment); !errors.Is(err, errInitialEnrichmentCallbackApplication) {
		t.Fatalf("Wait error=%v, want callback application failure", err)
	}
	if got := enrichment.inFlight; got != 0 {
		t.Fatalf("inFlight=%d after unapplied load finalization", got)
	}
}

func TestInitialEnrichmentEventLoadFinalizeAndStopRaceIsBounded(t *testing.T) {
	for run := 0; run < 32; run++ {
		actor := newEnrichmentActor(t, protocol.ModeInsert)
		source := &controlledInitialZoxideSource{
			started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
			ignoreCtx: true, result: enrichmentSource("/zoxide"),
		}
		enrichment, err := newInitialEnrichment(context.Background(), actor, source, fzf.NewInputStream(nil))
		if err != nil {
			t.Fatalf("run %d new enrichment: %v", run, err)
		}
		if err := enrichment.Activate(1); err != nil {
			t.Fatalf("run %d Activate: %v", run, err)
		}
		result, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
		if err != nil {
			t.Fatalf("run %d event: %v", run, err)
		}
		backend := &pickerBackend{actor: actor, enrichment: enrichment, metrics: &pickerMetrics{}}
		if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
			t.Fatalf("run %d FinalizeEvent: %v", run, err)
		}
		if _, err := backend.LoadGeneration(context.Background(), sessionipc.LoadRequest{Generation: result.Effect.ReloadGeneration, EventID: result.EventID}); err != nil {
			t.Fatalf("run %d LoadGeneration: %v", run, err)
		}
		if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
			t.Fatalf("run %d FinalizeLoad: %v", run, err)
		}
		close(source.release)
		if err := enrichment.Stop(nil); err != nil {
			t.Fatalf("run %d Stop: %v", run, err)
		}
		if err := enrichment.Wait(); err != nil {
			t.Fatalf("run %d Wait: %v", run, err)
		}
	}
}
