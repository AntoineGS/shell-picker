package session

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestActorEnrichPublishesClonedSnapshotWithoutEffect(t *testing.T) {
	actor, _ := initializeActor(t)
	records := []candidate.Record{testRecord("enriched", "/enriched")}
	sources := candidate.SourceMetrics{
		LocalDuration:  3 * time.Millisecond,
		ZoxideDuration: 5 * time.Millisecond,
		ZoxideOutcome:  "cached",
		ZoxideAttempts: 2,
	}

	result, err := actor.Enrich(context.Background(), 1, records, sources)
	if err != nil {
		t.Fatalf("Enrich() = %v", err)
	}
	if result.Snapshot.Generation() != 2 {
		t.Fatalf("generation = %d, want 2", result.Snapshot.Generation())
	}
	if result.Snapshot.State().Prompt != "[I] " || string(result.Snapshot.State().Location.Path) != "/start" {
		t.Fatalf("state = %+v, want initial state", result.Snapshot.State())
	}
	if result.Effect != (protocol.Effect{}) {
		t.Fatalf("effect = %+v, want empty effect", result.Effect)
	}
	if result.Metrics.Sources != sources || result.Metrics.QueueWait < 0 || result.Metrics.TransformDuration < 0 {
		t.Fatalf("metrics = %+v, want source metrics and durations", result.Metrics)
	}

	key := records[0].Wire().Bytes()
	records[0].Path[0] = 'X'
	records[0].Target.Path[0] = 'X'
	records[0].Payload = "mutated-input"
	resultRecords := result.Snapshot.Records()
	resultRecords[0].Path[0] = 'Y'
	resultRecords[0].Target.Path[0] = 'Y'
	resultRecords[0].Payload = "mutated-output"
	framed := result.Snapshot.FramedRecords()
	framed[0] = 'X'

	current, err := actor.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	if current.Generation() != 2 || len(current.Records()) != 1 {
		t.Fatalf("current snapshot = %+v, want enriched snapshot", current)
	}
	if got := current.Records()[0]; got.Display != "enriched" || string(got.Path) != "/enriched" ||
		string(got.Target.Path) != "/enriched" || got.Payload != protocol.EncodePath([]byte("/enriched")) {
		t.Fatalf("current record = %+v, input/output alias leaked", got)
	}
	if _, err := actor.ResolveCurrent(context.Background(), key); err != nil {
		t.Fatalf("ResolveCurrent() after output mutation: %v", err)
	}
}

type enrichOutcome struct {
	result TransitionResult
	err    error
}

func asyncEnrich(actor *Actor, ctx context.Context, baseGeneration uint64, records []candidate.Record, sources candidate.SourceMetrics) <-chan enrichOutcome {
	done := make(chan enrichOutcome, 1)
	go func() {
		result, err := actor.Enrich(ctx, baseGeneration, records, sources)
		done <- enrichOutcome{result: result, err: err}
	}()
	return done
}

func awaitEnrich(t *testing.T, done <-chan enrichOutcome) enrichOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(2 * time.Second):
		t.Fatal("Enrich did not reply")
		return enrichOutcome{}
	}
}

func initializeSessionActor(t *testing.T, sessionCtx context.Context) (*Actor, *controlledGenerator) {
	t.Helper()
	generator := newControlledGenerator()
	actor := New(sessionCtx, generator.Generate)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	pending := asyncApply(actor, context.Background(), testProposal(0, testState("/start", protocol.ModeInsert, "[I] "), true, protocol.Effect{}))
	call := generator.Next(t)
	call.Complete([]candidate.Record{testRecord("start", "/start")}, nil)
	if outcome := awaitApply(t, pending); outcome.err != nil {
		t.Fatalf("initialize Apply() = %v", outcome.err)
	}
	return actor, generator
}

func TestActorEnrichRejectsStaleAndCanceledContexts(t *testing.T) {
	actor, _ := initializeActor(t)
	records := []candidate.Record{testRecord("replacement", "/replacement")}

	if _, err := actor.Enrich(context.Background(), 0, records, candidate.SourceMetrics{}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("Enrich(stale) = %v, want %v", err, ErrStaleGeneration)
	}
	if _, err := actor.Enrich(nil, 1, records, candidate.SourceMetrics{}); !errors.Is(err, errNilContext) {
		t.Fatalf("Enrich(nil) = %v, want %v", err, errNilContext)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := actor.Enrich(canceled, 1, records, candidate.SourceMetrics{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Enrich(canceled) = %v, want %v", err, context.Canceled)
	}

	current, err := actor.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	if current.Generation() != 1 || current.Records()[0].Display != "start" {
		t.Fatalf("rejected enrich changed current snapshot = %+v", current)
	}
}

func TestActorEnrichReturnsSessionCancellationAndClose(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancelCause(context.Background())
	actor, _ := initializeSessionActor(t, sessionCtx)

	sessionErr := errors.New("session stopped")
	cancelSession(sessionErr)
	if _, err := actor.Enrich(context.Background(), 1, []candidate.Record{testRecord("ignored", "/ignored")}, candidate.SourceMetrics{}); !errors.Is(err, sessionErr) {
		t.Fatalf("Enrich(session canceled) = %v, want %v", err, sessionErr)
	}
	if err := actor.Close(); err != nil {
		t.Fatalf("Close() after session cancellation = %v", err)
	}

	closedActor, _ := initializeActor(t)
	if err := closedActor.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if _, err := closedActor.Enrich(context.Background(), 1, nil, candidate.SourceMetrics{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Enrich(after close) = %v, want %v", err, ErrClosed)
	}
}

func TestActorEnrichRejectsWhileNavigationBuildIsPendingWithoutSupersedingIt(t *testing.T) {
	actor, generator := initializeActor(t)
	navigation := asyncApply(actor, context.Background(), testProposal(
		1, testState("/navigation", protocol.ModeNormal, "navigation"), true, protocol.Effect{},
	))
	call := generator.Next(t)

	enrich := awaitEnrich(t, asyncEnrich(actor, context.Background(), 1,
		[]candidate.Record{testRecord("enriched", "/enriched")}, candidate.SourceMetrics{}))
	if !errors.Is(enrich.err, ErrTransitionPending) {
		t.Fatalf("Enrich(while navigation pending) = %v, want %v", enrich.err, ErrTransitionPending)
	}
	select {
	case <-call.ctx.Done():
		t.Fatal("enrichment canceled the pending navigation build")
	default:
	}

	call.Complete([]candidate.Record{testRecord("navigation", "/navigation")}, nil)
	if outcome := awaitApply(t, navigation); outcome.err != nil || outcome.result.Snapshot.Generation() != 2 {
		t.Fatalf("navigation after rejected enrichment = %+v", outcome)
	}
}

func TestActorEnrichRejectsAtGenerationLimitWithoutPublication(t *testing.T) {
	actor := &Actor{
		commands:  make(chan any),
		done:      make(chan struct{}),
		closeWait: make(chan struct{}),
		cleanup:   rollback,
		nextID:    math.MaxUint64,
	}
	go actor.run(context.Background(), nil)

	result, err := actor.Enrich(context.Background(), 0,
		[]candidate.Record{testRecord("ignored", "/ignored")}, candidate.SourceMetrics{})
	if !errors.Is(err, errGenerationLimit) {
		t.Fatalf("Enrich(at generation limit) = %v, want %v", err, errGenerationLimit)
	}
	if result.Snapshot.Generation() != 0 || result.Snapshot.RecordCount() != 0 ||
		result.Effect != (protocol.Effect{}) || result.Metrics != (TransitionMetrics{}) {
		t.Fatalf("generation-limit result = %+v, want zero result", result)
	}
	if err := actor.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestActorNavigationUsesEnrichedGenerationAsItsBase(t *testing.T) {
	actor, generator := initializeActor(t)
	enriched := awaitEnrich(t, asyncEnrich(actor, context.Background(), 1,
		[]candidate.Record{testRecord("enriched", "/enriched")}, candidate.SourceMetrics{}))
	if enriched.err != nil || enriched.result.Snapshot.Generation() != 2 {
		t.Fatalf("Enrich() = %+v", enriched)
	}

	navigation := asyncApply(actor, context.Background(), testProposal(
		2, testState("/next", protocol.ModeNormal, "next"), true, protocol.Effect{},
	))
	call := generator.Next(t)
	if call.request.Generation != 3 {
		t.Fatalf("navigation build generation = %d, want 3", call.request.Generation)
	}
	call.Complete([]candidate.Record{testRecord("next", "/next")}, nil)
	outcome := awaitApply(t, navigation)
	if outcome.err != nil || outcome.result.Snapshot.Generation() != 3 || outcome.result.Effect.ReloadGeneration != 3 {
		t.Fatalf("navigation after enrichment = %+v", outcome)
	}
}

func TestActorNavigationPublishesBeforeOldBaseEnrichmentIsSubmitted(t *testing.T) {
	actor, generator := initializeActor(t)
	navigation := asyncApply(actor, context.Background(), testProposal(
		1, testState("/navigation", protocol.ModeNormal, "navigation"), true, protocol.Effect{},
	))
	call := generator.Next(t)

	call.Complete([]candidate.Record{testRecord("navigation", "/navigation")}, nil)
	navigationOutcome := awaitApply(t, navigation)
	if navigationOutcome.err != nil || navigationOutcome.result.Snapshot.Generation() != 2 {
		t.Fatalf("navigation = %+v", navigationOutcome)
	}

	enrich := awaitEnrich(t, asyncEnrich(actor, context.Background(), 1,
		[]candidate.Record{testRecord("late-enrichment", "/late-enrichment")}, candidate.SourceMetrics{}))
	if !errors.Is(enrich.err, ErrStaleGeneration) {
		t.Fatalf("Enrich(old base after navigation publish) = %v, want %v", enrich.err, ErrStaleGeneration)
	}

	current, err := actor.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	if current.Generation() != 2 || string(current.State().Location.Path) != "/navigation" ||
		len(current.Records()) != 1 || current.Records()[0].Display != "navigation" {
		t.Fatalf("current after stale enrichment = %+v, want navigation snapshot", current)
	}
}

func TestActorEnrichmentAndNavigationOrderHasOneSerializedWinner(t *testing.T) {
	t.Run("enrichment wins and navigation is stale", func(t *testing.T) {
		actor, generator := initializeActor(t)
		enrich := awaitEnrich(t, asyncEnrich(actor, context.Background(), 1,
			[]candidate.Record{testRecord("enriched", "/enriched")}, candidate.SourceMetrics{}))
		if enrich.err != nil || enrich.result.Snapshot.Generation() != 2 {
			t.Fatalf("Enrich() = %+v", enrich)
		}

		navigation := awaitApply(t, asyncApply(actor, context.Background(), testProposal(
			1, testState("/stale", protocol.ModeNormal, "stale"), true, protocol.Effect{},
		)))
		if !errors.Is(navigation.err, ErrStaleGeneration) {
			t.Fatalf("navigation after enrichment = %v, want %v", navigation.err, ErrStaleGeneration)
		}
		select {
		case call := <-generator.started:
			t.Fatalf("stale navigation started a build for %q", call.request.Location.Path)
		default:
		}
	})

	t.Run("navigation wins and enrichment is pending", func(t *testing.T) {
		actor, generator := initializeActor(t)
		navigation := asyncApply(actor, context.Background(), testProposal(
			1, testState("/navigation", protocol.ModeNormal, "navigation"), true, protocol.Effect{},
		))
		call := generator.Next(t)

		enrich := awaitEnrich(t, asyncEnrich(actor, context.Background(), 1,
			[]candidate.Record{testRecord("enriched", "/enriched")}, candidate.SourceMetrics{}))
		if !errors.Is(enrich.err, ErrTransitionPending) {
			t.Fatalf("Enrich while navigation is pending = %v, want %v", enrich.err, ErrTransitionPending)
		}
		select {
		case <-call.ctx.Done():
			t.Fatal("enrichment canceled the navigation winner")
		default:
		}

		call.Complete([]candidate.Record{testRecord("navigation", "/navigation")}, nil)
		if outcome := awaitApply(t, navigation); outcome.err != nil || outcome.result.Snapshot.Generation() != 2 {
			t.Fatalf("navigation winner = %+v", outcome)
		}
	})
}

func TestActorEnrichRepliesWhileCloseRetiresNavigation(t *testing.T) {
	actor, generator := initializeActor(t)
	navigation := asyncApply(actor, context.Background(), testProposal(
		1, testState("/navigation", protocol.ModeNormal, "navigation"), true, protocol.Effect{},
	))
	call := generator.Next(t)
	closeDone := make(chan error, 1)
	go func() { closeDone <- actor.Close() }()

	select {
	case <-call.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not retire the navigation build")
	}
	enrich := awaitEnrich(t, asyncEnrich(actor, context.Background(), 1,
		[]candidate.Record{testRecord("ignored", "/ignored")}, candidate.SourceMetrics{}))
	if !errors.Is(enrich.err, ErrClosed) {
		t.Fatalf("Enrich while Close is retiring navigation = %v, want %v", enrich.err, ErrClosed)
	}

	call.Complete(nil, context.Canceled)
	if outcome := awaitApply(t, navigation); !errors.Is(outcome.err, ErrClosed) {
		t.Fatalf("navigation while Close is retiring = %v, want %v", outcome.err, ErrClosed)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not complete")
	}
}

func TestActorEnrichAcceptedBeforeSessionCancellationReturnsAuthoritativeCause(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancelCause(context.Background())
	actor, _ := initializeSessionActor(t, sessionCtx)

	blockedReply := make(chan snapshotReply)
	accepted := make(chan struct{})
	go func() {
		actor.commands <- currentCommand{ctx: context.Background(), reply: blockedReply}
		close(accepted)
	}()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking command was not accepted")
	}

	enrich := asyncEnrich(actor, context.Background(), 1,
		[]candidate.Record{testRecord("ignored", "/ignored")}, candidate.SourceMetrics{})
	sessionErr := errors.New("session stopped")
	cancelSession(sessionErr)
	select {
	case outcome := <-enrich:
		t.Fatalf("Enrich replied before blocking command release: %+v", outcome)
	default:
	}
	<-blockedReply

	outcome := awaitEnrich(t, enrich)
	if !errors.Is(outcome.err, sessionErr) {
		t.Fatalf("Enrich() = %v, want %v", outcome.err, sessionErr)
	}
	if err := actor.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}
