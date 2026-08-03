package integration

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/process"
)

func validateRealZoxideDiscardedTrace(events []traceEvent, generation uint64) error {
	var terminals []traceEvent
	for _, event := range events {
		if event.Event == "zoxide.enrichment" {
			terminals = append(terminals, event)
		}
	}
	if len(terminals) != 1 {
		return fmt.Errorf("zoxide enrichment terminals=%d events=%+v", len(terminals), events)
	}
	got := terminals[0]
	if got.Outcome != "discarded" || got.Generation != generation || got.CandidateCount != 0 || got.ZoxideOutcome != "cancelled" {
		return fmt.Errorf("discarded zoxide enrichment=%+v", got)
	}
	return nil
}

func validateRealFZFAbort(waitErr error, closed traceEvent, result []byte) error {
	if waitErr != nil {
		return fmt.Errorf("picker wait: %w", waitErr)
	}
	if closed.Outcome != "aborted" {
		return fmt.Errorf("picker close outcome=%q, want aborted", closed.Outcome)
	}
	if len(result) != 0 {
		return fmt.Errorf("picker abort produced result %q", result)
	}
	return nil
}

func waitForRealFZFResultFinal(t *testing.T, term terminalSession, generation uint64) int {
	t.Helper()
	beforeSlash := len(term.Output())
	slashCallbacks := traceCount(term.TraceEvents(), "callback.event", "sl")
	if err := term.Send([]byte{'/'}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "sl", Count: slashCallbacks + 1})
	waitForTerminalTextAfter(t, term, beforeSlash, "[Invalid Path]")

	beforeRestore := len(term.Output())
	restoreCallbacks := traceCount(term.TraceEvents(), "callback.event", "rs")
	loadBarrierCount := resultFinalLoadBarrierCount(term.TraceEvents(), generation)
	if err := term.Send([]byte{0x7f}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "rs", Count: restoreCallbacks + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Operation: "ok", Generation: generation, Count: loadBarrierCount})
	return beforeRestore
}

type resultFinalTerminalStub struct {
	output   []byte
	events   []traceEvent
	barriers []barrier
}

func (term *resultFinalTerminalStub) Send(input []byte) error {
	if bytes.Equal(input, []byte{'/'}) {
		term.output = append(term.output, []byte("[Invalid Path]")...)
	}
	return nil
}

func (term *resultFinalTerminalStub) Resize(uint16, uint16) error { return nil }

func (term *resultFinalTerminalStub) WaitBarrier(_ context.Context, wanted barrier) traceEvent {
	term.barriers = append(term.barriers, wanted)
	return traceEvent{Event: wanted.Event, Generation: wanted.Generation, Outcome: wanted.Operation}
}

func (term *resultFinalTerminalStub) TraceEvents() []traceEvent {
	return append([]traceEvent(nil), term.events...)
}

func (*resultFinalTerminalStub) AssertProcessTopology(*testing.T) {}

func (*resultFinalTerminalStub) TrackLiveDescendants(*testing.T) []trackedProcess { return nil }

func (*resultFinalTerminalStub) AssertTrackedProcessesGone(*testing.T, []trackedProcess) {}

func (*resultFinalTerminalStub) PID() int { return 0 }

func (term *resultFinalTerminalStub) Output() []byte {
	return append([]byte(nil), term.output...)
}

func (term *resultFinalTerminalStub) ResultBytes() []byte { return term.Output() }

func (*resultFinalTerminalStub) WaitOutputAfter(context.Context, int) {}

func (*resultFinalTerminalStub) CloseInput() error { return nil }

func (*resultFinalTerminalStub) Wait(context.Context) error { return nil }

func (*resultFinalTerminalStub) Close() error { return nil }

func TestWaitForRealFZFResultFinalDoesNotRequirePreviewFinished(t *testing.T) {
	term := &resultFinalTerminalStub{events: []traceEvent{
		{Event: "callback.event", Outcome: "sl"},
		{Event: "callback.event", Outcome: "rs"},
		{Event: "callback.load", Generation: 2, Outcome: "ok"},
	}}
	waitForRealFZFResultFinal(t, term, 2)
	if len(term.barriers) != 3 {
		t.Fatalf("barriers=%+v, want slash, restore, and load barriers", term.barriers)
	}
	for _, barrier := range term.barriers {
		if barrier.Event == "preview.finished" {
			t.Fatalf("unexpected preview barrier=%+v", barrier)
		}
	}
	load := term.barriers[2]
	if load.Event != "callback.load" || load.Operation != "ok" || load.Generation != 2 || load.Count != 2 {
		t.Fatalf("result-final load barrier=%+v, want generation 2 count 2 with ok outcome", load)
	}
}

func assertRealZoxideEnrichmentTrace(t *testing.T, events []traceEvent, lifecycle string, generation uint64, outcomes ...string) {
	t.Helper()
	var pending, terminal []traceEvent
	for _, event := range events {
		switch {
		case event.Event == "generation.publish" && event.Generation == 1:
			pending = append(pending, event)
		case event.Event == "zoxide.enrichment":
			terminal = append(terminal, event)
		}
	}
	if len(pending) != 1 {
		t.Fatalf("initial zoxide publication=%+v", pending)
	}
	assertRealZoxidePendingPublication(t, pending[0])
	if len(terminal) != 1 {
		t.Fatalf("zoxide enrichment terminals=%d events=%+v", len(terminal), events)
	}
	got := terminal[0]
	wantCandidates := 0
	if lifecycle == "published" {
		wantCandidates = 5
	}
	if got.Outcome != lifecycle || got.Generation != generation || got.CandidateCount != wantCandidates {
		t.Fatalf("zoxide enrichment terminal=%+v", got)
	}
	assertRealZoxideTerminalCounters(t, got, 1)
	for _, want := range outcomes {
		if got.ZoxideOutcome == want {
			return
		}
	}
	t.Fatalf("zoxide outcome=%q, want one of %q", got.ZoxideOutcome, outcomes)
}

func assertRealZoxideDiscardedTrace(t *testing.T, events []traceEvent, generation uint64, outcomes ...string) {
	t.Helper()
	var pending, terminal []traceEvent
	for _, event := range events {
		if event.Event == "generation.publish" && event.Generation == 1 {
			pending = append(pending, event)
		}
		if event.Event == "zoxide.enrichment" {
			terminal = append(terminal, event)
		}
	}
	if len(pending) != 1 {
		t.Fatalf("initial zoxide publication=%+v", pending)
	}
	assertRealZoxidePendingPublication(t, pending[0])
	if len(terminal) != 1 {
		t.Fatalf("zoxide enrichment terminals=%d events=%+v", len(terminal), events)
	}
	got := terminal[0]
	if err := validateRealZoxideDiscardedTrace(events, generation); err != nil {
		t.Fatal(err)
	}
	assertRealZoxideTerminalCounters(t, got, 1)
	if len(outcomes) != 0 && outcomes[0] != "cancelled" {
		t.Fatalf("discarded zoxide outcome=%q, want cancelled", outcomes[0])
	}
}

func assertRealZoxideNavigationGenerationsNotRun(t *testing.T, events []traceEvent, generations ...uint64) {
	t.Helper()
	for _, generation := range generations {
		count := 0
		for _, event := range events {
			if event.Event != "generation.publish" || event.Generation != generation {
				continue
			}
			count++
			if event.ZoxideOutcome != "not-run" || event.ZoxideAttempts != 0 || event.ZoxideStarts != 0 ||
				event.ZoxideExits != 0 || event.ZoxideProcesses != 0 || event.ZoxideLive != 0 || event.ZoxideMaxLive != 0 ||
				event.CandidateCount != 3 || event.ZoxidePolicy != "cached" || event.Outcome != "ok" {
				t.Fatalf("navigation generation %d unexpectedly ran zoxide: %+v", generation, event)
			}
		}
		if count != 1 {
			t.Fatalf("navigation generation %d publication count=%d; events=%+v", generation, count, events)
		}
	}
}

func assertRealZoxidePendingPublication(t *testing.T, event traceEvent) {
	t.Helper()
	if event.CandidateCount != 3 || event.Outcome != "ok" || event.ZoxidePolicy != "cached" ||
		event.ZoxideOutcome != "pending" || event.ZoxideAttempts != 0 || event.ZoxideStarts != 0 ||
		event.ZoxideExits != 0 || event.ZoxideProcesses != 0 || event.ZoxideLive != 0 || event.ZoxideMaxLive != 0 {
		t.Fatalf("initial zoxide publication=%+v", event)
	}
}

func assertRealZoxideTerminalCounters(t *testing.T, event traceEvent, wantProcesses int) {
	t.Helper()
	if event.ZoxidePolicy != "cached" || event.ZoxideAttempts != wantProcesses || event.ZoxideStarts != wantProcesses ||
		event.ZoxideExits != wantProcesses || event.ZoxideProcesses != wantProcesses || event.ZoxideLive != 0 ||
		event.ZoxideMaxLive != wantProcesses {
		t.Fatalf("zoxide terminal counters=%+v", event)
	}
}

func assertRealZoxideGenerationSequence(t *testing.T, events []traceEvent, want ...uint64) {
	t.Helper()
	got := make([]uint64, 0)
	for _, event := range events {
		if event.Event == "generation.publish" {
			got = append(got, event.Generation)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("generation publication sequence=%v want %v; events=%+v", got, want, events)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("generation publication sequence=%v want %v; events=%+v", got, want, events)
		}
	}
}

func assertRealZoxideAccepted(t *testing.T, term terminalSession, target string) {
	t.Helper()
	output := term.ResultBytes()
	want := append([]byte(target), 0)
	if bytes.Count(output, []byte{0}) != 1 || !bytes.Contains(output, want) {
		t.Fatalf("picker output does not contain exactly one canonical accepted target %q: %q", target, output)
	}
}

func TestValidateRealZoxideDiscardedTraceRequiresCancelledTerminal(t *testing.T) {
	for _, terminal := range []string{"failed", "published", ""} {
		t.Run(terminal, func(t *testing.T) {
			events := []traceEvent{{
				Event: "zoxide.enrichment", Outcome: "discarded", Generation: 1,
				CandidateCount: 0, ZoxideOutcome: terminal,
			}}
			if err := validateRealZoxideDiscardedTrace(events, 1); err == nil {
				t.Fatalf("terminal %q was accepted", terminal)
			}
		})
	}
}

func TestValidateRealZoxideDiscardedTraceAcceptsCancelledTerminal(t *testing.T) {
	events := []traceEvent{{
		Event: "zoxide.enrichment", Outcome: "discarded", Generation: 1,
		CandidateCount: 0, ZoxideOutcome: "cancelled",
	}}
	if err := validateRealZoxideDiscardedTrace(events, 1); err != nil {
		t.Fatalf("cancelled terminal rejected: %v", err)
	}
}

func TestValidateRealFZFAbortRequiresCleanWait(t *testing.T) {
	closed := traceEvent{Event: "session.close", Outcome: "aborted"}
	if err := validateRealFZFAbort(nil, closed, nil); err != nil {
		t.Fatalf("clean abort rejected: %v", err)
	}
	if err := validateRealFZFAbort(process.ErrWaitDelay, closed, nil); err == nil {
		t.Fatal("process.ErrWaitDelay accepted as a clean abort")
	}
}
