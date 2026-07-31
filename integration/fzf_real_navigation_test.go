package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFPickerNavigationAndNormalMode(t *testing.T) {
	t.Run("exact child slash preserves insert", testRealFZFExactChildSlash)
	t.Run("invalid slash restores after edit", testRealFZFInvalidSlashRestore)
	t.Run("normal printable keys and escape", testRealFZFNormalInputAndEscape)
	t.Run("normal paging directions", testRealFZFNormalPaging)
	t.Run("insert paging directions", testRealFZFInsertPaging)
	t.Run("normal first and last", testRealFZFNormalFirstLast)
}

func testRealFZFExactChildSlash(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "exact child navigation")
	if err := os.Mkdir(filepath.Join(fixture.cwd, "alpha", "insert-still-active"), 0o700); err != nil {
		t.Fatal(err)
	}
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	waitForTerminalText(t, term, "5/5")

	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalText(t, term, "[I] alpha")
	previewBefore := traceCount(term.TraceEvents(), "preview.dispatch", "")
	sendAndWait(t, term, []byte{'/'}, barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewBefore + 1})
	waitForTerminalText(t, term, "alpha"+string(os.PathSeparator))

	sendAndWait(t, term, []byte("insert-still-active/"), barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 3, Count: 1})
	waitForTerminalText(t, term, "insert-still-active"+string(os.PathSeparator))
}

func testRealFZFInvalidSlashRestore(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "invalid slash restore")
	removeFixtureCandidates(t, fixture)
	if err := os.Mkdir(filepath.Join(fixture.cwd, "foobar"), 0o700); err != nil {
		t.Fatal(err)
	}
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	waitForTerminalText(t, term, "3/3")

	if err := term.Send([]byte("foo")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalText(t, term, "[I] foo")
	waitForTerminalText(t, term, "foobar")
	waitForTerminalText(t, term, "1/3")

	restoreBefore := traceCount(term.TraceEvents(), "callback.event", "rs")
	loadBefore := traceCount(term.TraceEvents(), "callback.load", "")
	beforeSlash := len(term.Output())
	sendAndWait(t, term, []byte{'/'}, barrier{Event: "callback.event", Operation: "sl", Count: 1})
	waitForTerminalTextAfter(t, term, beforeSlash, "0/0")
	waitForTerminalTextCountAfter(t, term, beforeSlash, "[Invalid Path]", 2)
	assertTraceCount(t, term.TraceEvents(), "callback.event", "rs", restoreBefore)

	previewBefore := traceCount(term.TraceEvents(), "preview.dispatch", "")
	beforeEdit := len(term.Output())
	sendAndWait(t, term, []byte{0x7f}, barrier{Event: "callback.event", Operation: "rs", Count: restoreBefore + 1})
	load := term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadBefore + 1})
	preview := term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewBefore + 1})
	waitForTerminalTextAfter(t, term, beforeEdit, "foobar")
	assertTraceCount(t, term.TraceEvents(), "callback.event", "rs", restoreBefore+1)
	if load.Generation != 1 || preview.Outcome != "ok" || preview.Renderer == "" {
		t.Fatalf("restore load/preview=%+v/%+v; events=%+v", load, preview, term.TraceEvents())
	}
}

func testRealFZFNormalInputAndEscape(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "normal ignored input")
	removeFixtureCandidates(t, fixture)
	if err := os.Mkdir(filepath.Join(fixture.cwd, "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	waitForTerminalText(t, term, "3/3")
	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	waitForTerminalText(t, term, "[I] alpha")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	waitForTerminalText(t, term, "[N] alpha")

	if err := term.Send([]byte{'b', 'Z', '7', ':', '+', '\\'}); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	beforeResize := len(term.Output())
	if err := term.Resize(121, 35); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeResize)
	waitForTerminalTextAfter(t, term, beforeResize, "[I] alpha")
	waitForTerminalTextAfter(t, term, beforeResize, "1/3")
	if visible := visibleTerminalOutput(term.Output()[beforeResize:]); bytes.Contains(visible, []byte(`alphabZ7:+\`)) {
		t.Fatalf("ignored Normal keys mutated query: %q", term.Output()[beforeResize:])
	}

	beforeNormal := len(term.Output())
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
	waitForTerminalTextAfter(t, term, beforeNormal, "[N] alpha")
	waitForTerminalTextAfter(t, term, beforeNormal, "1/3")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 3})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	closed := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	if closed.Outcome != "aborted" || bytes.Contains(term.Output(), []byte{0}) {
		t.Fatalf("second Escape close/output=%+v/%q", closed, term.Output())
	}
}

func testRealFZFNormalPaging(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		fixture := newRealFZFFixture(t, requireRealFZF(t), "normal list paging")
		removeFixtureCandidates(t, fixture)
		for index := 0; index < 36; index++ {
			name := fmt.Sprintf("item-%02d.txt", index)
			content := fmt.Sprintf("LIST-PREVIEW-%02d\n", index)
			if err := os.WriteFile(filepath.Join(fixture.cwd, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		term := fixture.startSized(t, protocol.PickerCP, []string{"PATH=" + t.TempDir()}, 80, 18)
		defer term.Close()
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
		if err := term.Send([]byte("item-")); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
		waitForTerminalText(t, term, "LIST-PREVIEW-00")
		beforeNormal := len(term.Output())
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N] item-")

		beforeDown := len(term.Output())
		previewCount := traceCount(term.TraceEvents(), "preview.dispatch", "")
		finishedCount := traceCount(term.TraceEvents(), "preview.finished", "")
		if err := term.Send([]byte{0x04}); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
		down := waitForChangedNavigationMarkerAfter(t, term, beforeDown, "LIST", 0)

		beforeUp := len(term.Output())
		if err := term.Send([]byte{0x15}); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 2})
		term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 2})
		up := waitForChangedNavigationMarkerAfter(t, term, beforeUp, "LIST", down)
		if down <= 0 || up >= down {
			t.Fatalf("Ctrl-D/Ctrl-U visible markers=%d/%d, want down then up", down, up)
		}
	})

	t.Run("preview", func(t *testing.T) {
		fixture := newRealFZFFixture(t, requireRealFZF(t), "normal preview paging")
		removeFixtureCandidates(t, fixture)
		var content bytes.Buffer
		for index := 0; index < 60; index++ {
			fmt.Fprintf(&content, "SCROLL-PREVIEW-%02d line with deterministic content\n", index)
		}
		if err := os.WriteFile(filepath.Join(fixture.cwd, "preview-target.txt"), content.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		term := fixture.startSized(t, protocol.PickerCP, []string{"PATH=" + t.TempDir()}, 80, 18)
		defer term.Close()
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
		if err := term.Send([]byte("preview-target")); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
		waitForTerminalText(t, term, "SCROLL-PREVIEW-00")
		beforeNormal := len(term.Output())
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N] preview-target")

		beforeDown := len(term.Output())
		if err := term.Send([]byte{'.'}); err != nil {
			t.Fatal(err)
		}
		down := waitForChangedNavigationMarkerAfter(t, term, beforeDown, "SCROLL", 0)
		beforeUp := len(term.Output())
		if err := term.Send([]byte{','}); err != nil {
			t.Fatal(err)
		}
		up := waitForChangedNavigationMarkerAfter(t, term, beforeUp, "SCROLL", down)
		if down <= 0 || up >= down {
			t.Fatalf("period/comma visible preview markers=%d/%d, want down then up", down, up)
		}
	})
}

func testRealFZFInsertPaging(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "insert list paging")
	removeFixtureCandidates(t, fixture)
	for index := 0; index < 36; index++ {
		name := fmt.Sprintf("item-%02d.txt", index)
		content := fmt.Sprintf("LIST-PREVIEW-%02d\n", index)
		if err := os.WriteFile(filepath.Join(fixture.cwd, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	term := fixture.startSized(t, protocol.PickerCP, []string{"PATH=" + t.TempDir()}, 80, 18)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	if err := term.Send([]byte("item-")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalText(t, term, "LIST-PREVIEW-00")
	waitForTerminalText(t, term, "[I] item-")

	previewCount := traceCount(term.TraceEvents(), "preview.dispatch", "")
	finishedCount := traceCount(term.TraceEvents(), "preview.finished", "")
	callbackCount := traceCount(term.TraceEvents(), "callback.event", "")
	beforeDown := len(term.Output())
	if err := term.Send([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
	down := waitForChangedNavigationMarkerAfter(t, term, beforeDown, "LIST", 0)
	resizeAndWaitForRedraw(t, term, 81, 18)
	assertLatestModePromptAfter(t, term, beforeDown, "[I] item-", fmt.Sprintf("LIST-PREVIEW-%02d", down))
	assertTraceCount(t, term.TraceEvents(), "callback.event", "", callbackCount)

	previewCount = traceCount(term.TraceEvents(), "preview.dispatch", "")
	finishedCount = traceCount(term.TraceEvents(), "preview.finished", "")
	beforeUp := len(term.Output())
	if err := term.Send([]byte{0x15}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
	up := waitForChangedNavigationMarkerAfter(t, term, beforeUp, "LIST", down)
	resizeAndWaitForRedraw(t, term, 82, 18)
	assertLatestModePromptAfter(t, term, beforeUp, "[I] item-", fmt.Sprintf("LIST-PREVIEW-%02d", up))
	if down <= 0 || up >= down {
		t.Fatalf("Ctrl-D/Ctrl-U visible markers=%d/%d, want down then up", down, up)
	}
	assertTraceCount(t, term.TraceEvents(), "callback.event", "", callbackCount)
}

func testRealFZFNormalFirstLast(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "normal first and last")
	removeFixtureCandidates(t, fixture)
	for index := 0; index < 36; index++ {
		name := fmt.Sprintf("item-%02d.txt", index)
		content := fmt.Sprintf("LIST-PREVIEW-%02d\n", index)
		if err := os.WriteFile(filepath.Join(fixture.cwd, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	term := fixture.startSized(t, protocol.PickerCP, []string{"PATH=" + t.TempDir()}, 80, 18)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	if err := term.Send([]byte("item-")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalText(t, term, "LIST-PREVIEW-00")
	waitForTerminalText(t, term, "[I] item-")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	waitForTerminalText(t, term, "[N] item-")

	previewCount := traceCount(term.TraceEvents(), "preview.dispatch", "")
	finishedCount := traceCount(term.TraceEvents(), "preview.finished", "")
	beforeLast := len(term.Output())
	if err := term.Send([]byte{'G'}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
	last := waitForChangedNavigationMarkerAfter(t, term, beforeLast, "LIST", 0)
	if last != 35 {
		t.Fatalf("G selected marker=%d, want 35", last)
	}

	beforePrefix := len(term.Output())
	if err := term.Send([]byte{'g'}); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforePrefix, "▌ item-35.txt")
	if marker, changed := changedNavigationMarker(visibleTerminalOutput(term.Output()[beforePrefix:]), "LIST", 35); changed {
		t.Fatalf("first g changed preview marker to %d, want 35", marker)
	}
	firstGEnd := len(term.Output())
	firstGOutput := visibleTerminalOutput(term.Output()[beforePrefix:firstGEnd])
	if marker, changed := changedNavigationMarker(firstGOutput, "LIST", 35); changed {
		t.Fatalf("first g changed preview marker to %d, want 35", marker)
	}
	if item, ok := latestSelectedNavigationItem(firstGOutput); !ok || item != "item-35.txt" {
		t.Fatalf("first g selected item=%q/%t, want item-35.txt/true", item, ok)
	}
	// Complete the prefix with a consumed different key before refreshing the prompt.
	beforeCancel := len(term.Output())
	if err := term.Send([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeCancel)
	beforePrompt := len(term.Output())
	if item, ok := latestSelectedNavigationItem(visibleTerminalOutput(term.Output()[beforePrefix:beforePrompt])); !ok || item != "item-35.txt" {
		t.Fatalf("cancelled first g selected item=%q/%t, want item-35.txt/true", item, ok)
	}
	resizeAndWaitForRedraw(t, term, 81, 18)
	assertLatestModePromptAfter(t, term, beforePrefix, "[N] item-", "▌ item-35.txt")

	previewCount = traceCount(term.TraceEvents(), "preview.dispatch", "")
	finishedCount = traceCount(term.TraceEvents(), "preview.finished", "")
	beforeReenter := len(term.Output())
	if err := term.Send([]byte{'g'}); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeReenter)
	beforeFirst := len(term.Output())
	if err := term.Send([]byte{'g'}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
	first := waitForChangedNavigationMarkerAfter(t, term, beforeFirst, "LIST", 35)
	resizeAndWaitForRedraw(t, term, 82, 18)
	assertLatestModePromptAfter(t, term, beforeFirst, "[N] item-", fmt.Sprintf("LIST-PREVIEW-%02d", first))
	if first != 0 {
		t.Fatalf("gg selected marker=%d, want 0", first)
	}
}

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

func assertTraceCount(t *testing.T, events []traceEvent, name, outcome string, want int) {
	t.Helper()
	if got := traceCount(events, name, outcome); got != want {
		t.Fatalf("trace %s/%s count=%d want %d; events=%+v", name, outcome, got, want, events)
	}
}

func TestChangedNavigationMarkerSkipsStaleContent(t *testing.T) {
	output := []byte("SCROLL-PREVIEW-00 stale redraw\nSCROLL-PREVIEW-12 paged redraw\n")
	got, ok := changedNavigationMarker(output, "SCROLL", 0)
	if !ok || got != 12 {
		t.Fatalf("changed marker=%d/%t, want 12/true", got, ok)
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
