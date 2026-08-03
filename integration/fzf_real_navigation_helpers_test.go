package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func removeFixtureCandidates(t *testing.T, fixture *realFZFFixture) {
	t.Helper()
	entries, err := os.ReadDir(fixture.cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(fixture.cwd, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
}

func traceCount(events []traceEvent, name, outcome string) int {
	count := 0
	for _, event := range events {
		if event.Event == name && (outcome == "" || event.Outcome == outcome) {
			count++
		}
	}
	return count
}

func traceCountGeneration(events []traceEvent, name string, generation uint64) int {
	count := 0
	for _, event := range events {
		if event.Event == name && event.Generation == generation {
			count++
		}
	}
	return count
}

func resultFinalLoadBarrierCount(events []traceEvent, generation uint64) int {
	return traceCountGeneration(events, "callback.load", generation) + 1
}

func assertTraceCount(t *testing.T, events []traceEvent, name, outcome string, want int) {
	t.Helper()
	if got := traceCount(events, name, outcome); got != want {
		t.Fatalf("trace %s/%s count=%d want %d; events=%+v", name, outcome, got, want, events)
	}
}

func TestResultFinalLoadBarrierUsesCurrentGenerationBaseline(t *testing.T) {
	events := []traceEvent{
		{Event: "callback.load", Generation: 1, Outcome: "ok"},
		{Event: "callback.load", Generation: 2, Outcome: "ok"},
		{Event: "callback.load", Generation: 2, Outcome: "ok"},
	}
	if got := resultFinalLoadBarrierCount(events, 2); got != 3 {
		t.Fatalf("result-final load barrier count=%d, want 3", got)
	}
}

func waitForChangedNavigationMarkerAfter(t *testing.T, term terminalSession, before int, prefix string, previous int) int {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if before <= len(output) {
			if value, ok := changedNavigationMarker(visibleTerminalOutput(output[before:]), prefix, previous); ok {
				return value
			}
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}

func changedNavigationMarker(output []byte, prefix string, previous int) (int, bool) {
	marker := regexp.MustCompile(regexp.QuoteMeta(prefix) + `-PREVIEW-([0-9]{2})`)
	for _, match := range marker.FindAllSubmatch(output, -1) {
		value, err := strconv.Atoi(string(match[1]))
		if err == nil && value != previous {
			return value, true
		}
	}
	return 0, false
}

func latestSelectedNavigationItem(output []byte) (string, bool) {
	marker := regexp.MustCompile(`▌ (item-[0-9]{2}\.txt)`)
	matches := marker.FindAllSubmatch(output, -1)
	if len(matches) == 0 {
		return "", false
	}
	return string(matches[len(matches)-1][1]), true
}

func assertLatestModePromptAfter(t *testing.T, term terminalSession, before int, want, evidence string) {
	t.Helper()
	waitForTerminalTextAfter(t, term, before, evidence)
	waitForTerminalTextAfter(t, term, before, want)
	raw := term.Output()
	if before >= len(raw) {
		t.Fatalf("action produced no output after %d bytes", before)
	}
	visible := visibleTerminalOutput(raw[before:])
	wantIndex := bytes.LastIndex(visible, []byte(want))
	if wantIndex < 0 {
		t.Fatalf("latest mode prompt lacks %q in output after %d bytes: %q", want, before, raw[before:])
	}
	separator := strings.Index(want, "] ")
	if separator < 0 {
		t.Fatalf("invalid mode prompt %q", want)
	}
	query := want[separator+2:]
	for _, prefix := range []string{"[I] ", "[N] ", "[A] ", "[A!] "} {
		candidate := []byte(prefix + query)
		if candidateIndex := bytes.LastIndex(visible, candidate); candidateIndex > wantIndex {
			t.Fatalf("latest mode prompt is %q, want %q; output=%q", prefix+query, want, raw[before:])
		}
	}
}

func resizeTerminal(t *testing.T, term terminalSession, columns, lines uint16) {
	t.Helper()
	if err := term.Resize(columns, lines); err != nil {
		t.Fatal(err)
	}
}

func resizeAndWaitForRedraw(t *testing.T, term terminalSession, columns, lines uint16) {
	t.Helper()
	before := len(term.Output())
	resizeTerminal(t, term, columns, lines)
	term.WaitOutputAfter(testContext(t), before)
}

func waitForTerminalTextCountAfter(t *testing.T, term terminalSession, before int, text string, count int) {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if before <= len(output) && bytes.Count(visibleTerminalOutput(output[before:]), []byte(text)) >= count {
			return
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}
