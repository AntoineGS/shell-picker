package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

	beforeQuery := len(term.Output())
	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalTextAfter(t, term, beforeQuery, "[I]")
	waitForTerminalTextAfter(t, term, beforeQuery, "alpha")
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

	beforeQuery := len(term.Output())
	if err := term.Send([]byte("foo")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalTextAfter(t, term, beforeQuery, "[I]")
	waitForTerminalTextAfter(t, term, beforeQuery, "foo")
	waitForTerminalTextAfter(t, term, beforeQuery, "foobar")
	waitForTerminalTextAfter(t, term, beforeQuery, "1/3")

	restoreBefore := traceCount(term.TraceEvents(), "callback.event", "rs")
	loadBefore := traceCount(term.TraceEvents(), "callback.load", "")
	beforeSlash := len(term.Output())
	sendAndWait(t, term, []byte{'/'}, barrier{Event: "callback.event", Operation: "sl", Count: 1})
	waitForTerminalTextAfter(t, term, beforeSlash, "[Invalid")
	waitForTerminalTextAfter(t, term, beforeSlash, "Path]")
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
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	waitForTerminalTextAfter(t, term, beforeQuery, "[I]")
	waitForTerminalTextAfter(t, term, beforeQuery, "alpha")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	waitForTerminalText(t, term, "[N]")

	if err := term.Send([]byte{'b', 'Z', '7', ':', '+', '\\'}); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	beforeResize := len(term.Output())
	if err := term.Resize(121, 35); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeResize)
	waitForTerminalTextAfter(t, term, beforeResize, "[I]")
	waitForTerminalTextAfter(t, term, beforeResize, "alpha")
	waitForTerminalTextAfter(t, term, beforeResize, "1/3")
	if visible := visibleTerminalOutput(term.Output()[beforeResize:]); bytes.Contains(visible, []byte(`alphabZ7:+\`)) {
		t.Fatalf("ignored Normal keys mutated query: %q", term.Output()[beforeResize:])
	}

	beforeNormal := len(term.Output())
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
	waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
	waitForTerminalTextAfter(t, term, beforeNormal, "alpha")
	waitForTerminalTextAfter(t, term, beforeNormal, "1/3")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 3})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	closed := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	if closed.Outcome != "aborted" || len(term.ResultBytes()) != 0 {
		t.Fatalf("second Escape close/result=%+v/%q", closed, term.ResultBytes())
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
		loadCount := traceCountGeneration(term.TraceEvents(), "callback.load", 1)
		finishedBeforeNormal := traceCount(term.TraceEvents(), "preview.finished", "")
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
		waitForTerminalTextAfter(t, term, beforeNormal, "item-")
		term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadCount + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedBeforeNormal + 1})

		beforeMove := len(term.Output())
		for range 2 {
			previewCount := traceCount(term.TraceEvents(), "preview.dispatch", "")
			finishedCount := traceCount(term.TraceEvents(), "preview.finished", "")
			if err := term.Send(keyDown); err != nil {
				t.Fatal(err)
			}
			term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
			term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
		}
		waitForTerminalTextAfter(t, term, beforeMove, "LIST-PREVIEW-00")

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
		loadCount := traceCountGeneration(term.TraceEvents(), "callback.load", 1)
		finishedBeforeNormal := traceCount(term.TraceEvents(), "preview.finished", "")
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
		waitForTerminalTextAfter(t, term, beforeNormal, "preview-target")
		term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadCount + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedBeforeNormal + 1})

		beforeMove := len(term.Output())
		for range 2 {
			previewCount := traceCount(term.TraceEvents(), "preview.dispatch", "")
			finishedCount := traceCount(term.TraceEvents(), "preview.finished", "")
			if err := term.Send(keyDown); err != nil {
				t.Fatal(err)
			}
			term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
			term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
		}
		waitForTerminalTextAfter(t, term, beforeMove, "SCROLL-PREVIEW-00")

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

	previewCount = traceCount(term.TraceEvents(), "preview.dispatch", "")
	finishedCount = traceCount(term.TraceEvents(), "preview.finished", "")
	beforeFirst := len(term.Output())
	if err := term.Send([]byte("gg")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewCount + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
	waitForTerminalTextAfter(t, term, beforeFirst, "LIST-PREVIEW-00")
	resizeAndWaitForRedraw(t, term, 82, 18)
	assertLatestModePromptAfter(t, term, beforeFirst, "[N] item-", "LIST-PREVIEW-00")
	if item, ok := latestSelectedNavigationItem(visibleTerminalOutput(term.Output()[beforeFirst:])); !ok || item != "item-00.txt" {
		t.Fatalf("gg selected item=%q/%t, want item-00.txt/true", item, ok)
	}
}

func TestChangedNavigationMarkerSkipsStaleContent(t *testing.T) {
	output := []byte("SCROLL-PREVIEW-00 stale redraw\nSCROLL-PREVIEW-12 paged redraw\n")
	got, ok := changedNavigationMarker(output, "SCROLL", 0)
	if !ok || got != 12 {
		t.Fatalf("changed marker=%d/%t, want 12/true", got, ok)
	}
}
