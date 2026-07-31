package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFPickerNavigationAndNormalMode(t *testing.T) {
	t.Run("exact child slash preserves insert", testRealFZFExactChildSlash)
	t.Run("invalid slash restores after edit", testRealFZFInvalidSlashRestore)
	t.Run("normal printable keys and escape", testRealFZFNormalInputAndEscape)
	t.Run("normal paging directions", testRealFZFNormalPaging)
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
	beforeResize := len(term.Output())
	if err := term.Resize(121, 35); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeResize)
	waitForTerminalTextAfter(t, term, beforeResize, "[N] alpha")
	waitForTerminalTextAfter(t, term, beforeResize, "1/3")
	if visible := visibleTerminalOutput(term.Output()[beforeResize:]); bytes.Contains(visible, []byte(`alphabZ7:+\`)) {
		t.Fatalf("ignored Normal keys mutated query: %q", term.Output()[beforeResize:])
	}

	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
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
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})

		beforeDown := len(term.Output())
		previewCount := traceCount(term.TraceEvents(), "preview.dispatch", "")
		if err := term.Send([]byte{0x04}); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
		down := waitForNavigationMarkerAfter(t, term, beforeDown, "LIST")

		beforeUp := len(term.Output())
		if err := term.Send([]byte{0x15}); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 2})
		up := waitForNavigationMarkerAfter(t, term, beforeUp, "LIST")
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
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})

		beforeDown := len(term.Output())
		if err := term.Send([]byte{'.'}); err != nil {
			t.Fatal(err)
		}
		down := waitForNavigationMarkerAfter(t, term, beforeDown, "SCROLL")
		beforeUp := len(term.Output())
		if err := term.Send([]byte{','}); err != nil {
			t.Fatal(err)
		}
		up := waitForNavigationMarkerAfter(t, term, beforeUp, "SCROLL")
		if down <= 0 || up >= down {
			t.Fatalf("period/comma visible preview markers=%d/%d, want down then up", down, up)
		}
	})
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

func waitForNavigationMarkerAfter(t *testing.T, term terminalSession, before int, prefix string) int {
	t.Helper()
	ctx := testContext(t)
	marker := regexp.MustCompile(prefix + `-PREVIEW-([0-9]{2})`)
	for {
		output := term.Output()
		if before <= len(output) {
			match := marker.FindSubmatch(visibleTerminalOutput(output[before:]))
			if len(match) == 2 {
				value, err := strconv.Atoi(string(match[1]))
				if err != nil {
					t.Fatal(err)
				}
				return value
			}
		}
		term.WaitOutputAfter(ctx, len(output))
	}
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
