package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFForcedFilesSearchFromCD(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "forced files search")
	databaseDir := filepath.Join(fixture.root, "xdg", "fsearch")
	if err := os.MkdirAll(databaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSyntheticFSearchDB(t, filepath.Join(databaseDir, "fsearch.db"), fixture.cwd,
		[]string{"first.txt", "second.txt"})
	term := fixture.startSized(t, protocol.PickerCD,
		[]string{"XDG_DATA_HOME=" + filepath.Join(fixture.root, "xdg")}, 120, 35)
	defer term.Close()
	defer func() {
		if t.Failed() {
			output := visibleTerminalOutput(term.Output())
			t.Logf("terminal tail=%q", output[max(0, len(output)-6000):])
			for _, event := range term.TraceEvents() {
				if event.Event == "callback.event" || event.Event == "callback.load" || event.Event == "generation.publish" {
					t.Logf("event=%s outcome=%s generation=%d count=%d", event.Event, event.Outcome, event.Generation, event.CandidateCount)
				}
			}
		}
	}()
	waitForTerminalText(t, term, "5/5")
	sendAndWait(t, term, []byte{0x1b, 0x06}, barrier{Event: "callback.event", Operation: "fa", Count: 1})
	waitForTerminalText(t, term, "FSearch · saved ")
	waitForTerminalText(t, term, "3/3")
	for i, name := range []string{"first.txt", "second.txt"} {
		if i > 0 {
			for range len("first.txt") {
				if err := term.Send([]byte{0x7f}); err != nil {
					t.Fatal(err)
				}
			}
		}
		before := len(term.Output())
		if err := term.Send([]byte(name)); err != nil {
			t.Fatal(err)
		}
		waitForTerminalTextAfter(t, term, before, "1/3")
		sendAndWait(t, term, keyCtrlSpace, barrier{Event: "callback.event", Operation: "mk", Count: i + 1})
	}
	// Browser round trip must preserve both marks and the unrestricted scope.
	before := len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 1})
	waitForTerminalTextAfter(t, term, before, "5/5")
	before = len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 2})
	waitForTerminalTextAfter(t, term, before, "1/3")
	sendAndWait(t, term, keyEnter, barrier{Event: "callback.event", Operation: "en", Count: 1})
	var exit *exec.ExitError
	if err := term.Wait(testContext(t)); !errors.As(err, &exit) || exit.ExitCode() != 10 {
		t.Fatalf("forced search exit=%v, want insertion exit 10", err)
	}
	want := filepath.Join(fixture.cwd, "first.txt") + "\x00" + filepath.Join(fixture.cwd, "second.txt") + "\x00"
	if !bytes.Equal(term.ResultBytes(), []byte(want)) {
		t.Fatalf("forced selection=%q, want %q", term.ResultBytes(), want)
	}
}

func TestRealFZFSourceRoundTripRetainsZoxide(t *testing.T) {
	fixture := newRealZoxideFixture(t, requireRealFZF(t))
	databaseDir := filepath.Join(fixture.root, "xdg", "fsearch")
	if err := os.MkdirAll(databaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSyntheticFSearchDB(t, filepath.Join(databaseDir, "fsearch.db"), fixture.cwd, nil)
	fixture.Release(t)
	term := fixture.startSizedWithZoxideTimeout(t, protocol.PickerCD, []string{
		"XDG_DATA_HOME=" + filepath.Join(fixture.root, "xdg"),
		"PATH=" + fixture.tools + string(os.PathListSeparator) + os.Getenv("PATH"),
		parityHelperEnvironment + "=" + realZoxideHelperMode,
		realZoxideStartedEnvironment + "=" + fixture.started,
		realZoxideReleaseEnvironment + "=" + fixture.release,
		realZoxideRootEnvironment + "=" + fixture.home,
	}, 120, 35, "0")
	defer term.Close()
	waitForTerminalText(t, term, "5/5")
	if err := term.Send([]byte("late-match")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "1/5")
	sendAndWait(t, term, keyCtrlSpace, barrier{Event: "callback.event", Operation: "mk", Count: 1})
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 1})
	waitForTerminalText(t, term, "FSearch · directories only")
	for range len("late-match") {
		if err := term.Send([]byte{0x7f}); err != nil {
			t.Fatal(err)
		}
	}
	if err := term.Send([]byte("no-match")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "[I] no-match")
	before := len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 2})
	waitForTerminalTextAfter(t, term, before, "1/5")
	sendAndWait(t, term, keyEnter, barrier{Event: "callback.event", Operation: "en", Count: 1})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, fixture.lateTarget)
}
