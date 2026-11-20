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
	if err := term.Send([]byte("foo")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "1/3")

	for attempt := range 20 {
		beforeInvalid := len(term.Output())
		sendAndWait(t, term, []byte{'/'}, barrier{Event: "callback.event", Operation: "sl", Count: attempt + 1})
		waitForTerminalTextAfter(t, term, beforeInvalid, "0/0")

		// The first edit restores the same generation. Deliver more typing
		// with it to exercise the coordinator's load reservation.
		beforeRestore := len(term.Output())
		loadBefore := traceCountGeneration(term.TraceEvents(), "callback.load", 1)
		// Backspace removes the slash and triggers restore. A second
		// backspace plus typing leaves the original query for the next run.
		if err := term.Send([]byte("\x7f\x7fo")); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "rs", Count: attempt + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadBefore + 1})
		waitForTerminalTextAfter(t, term, beforeRestore, "1/3")
	}

	// A subsequent callback must be able to acquire the coordinator and exit.
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, target)
}
