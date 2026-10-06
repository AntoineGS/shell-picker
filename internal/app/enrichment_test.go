package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

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
