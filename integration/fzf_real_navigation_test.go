package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFPickerNavigation(t *testing.T) {
	t.Run("exact child slash preserves insert", testRealFZFExactChildSlash)
	t.Run("invalid slash restores after edit", testRealFZFInvalidSlashRestore)
	t.Run("single Escape cancels from insert", testRealFZFSingleEscape)
	t.Run("insert paging directions", testRealFZFInsertPaging)
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
	waitForTerminalTextAfter(t, term, beforeSlash, "0/0")
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

func testRealFZFSingleEscape(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "single Escape")
	if err := os.Mkdir(filepath.Join(fixture.cwd, "iq aj"), 0o700); err != nil {
		t.Fatal(err)
	}
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	waitForTerminalText(t, term, "6/6")
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("iq aj")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "[I]")
	waitForTerminalTextAfter(t, term, beforeQuery, "1/6")
	if err := term.Send(keyEsc); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := term.Wait(ctx); err != nil {
		t.Fatalf("picker did not exit after one Escape: %v", err)
	}
	closed := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	if closed.Outcome != "aborted" || len(term.ResultBytes()) != 0 {
		t.Fatalf("single Escape close/result=%+v/%q", closed, term.ResultBytes())
	}
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

func TestChangedNavigationMarkerSkipsStaleContent(t *testing.T) {
	output := []byte("SCROLL-PREVIEW-00 stale redraw\nSCROLL-PREVIEW-12 paged redraw\n")
	got, ok := changedNavigationMarker(output, "SCROLL", 0)
	if !ok || got != 12 {
		t.Fatalf("changed marker=%d/%t, want 12/true", got, ok)
	}
}
