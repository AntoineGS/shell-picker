package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFRestoreSurvivesRapidTyping(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "rapid restore")
	removeFixtureCandidates(t, fixture)
	target := filepath.Join(fixture.cwd, "foobar")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	// Keep the CD coordinator, but finish the optional source before editing.
	term := fixture.start(t, protocol.PickerCD, []string{"PATH=" + t.TempDir()})
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "zoxide.enrichment", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 1})
	waitForTerminalText(t, term, "3/3")

	for attempt := range 20 {
		beforeNormal := len(term.Output())
		loadBeforeNormal := traceCountGeneration(term.TraceEvents(), "callback.load", 1)
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: attempt + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadBeforeNormal + 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N]")

		// Returning to Insert restores the same generation. Deliver that key
		// and more typing together: without wait a query search can replace
		// fzf's queued restore, stranding the coordinator's load reservation.
		beforeRestore := len(term.Output())
		loadBefore := traceCountGeneration(term.TraceEvents(), "callback.load", 1)
		if err := term.Send([]byte("ifoo")); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "mi", Count: attempt + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadBefore + 1})
		waitForTerminalTextAfter(t, term, beforeRestore, "[I]")
	}

	// A subsequent callback must be able to acquire the coordinator and exit.
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("\x17foo")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "1/3")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, target)
}
