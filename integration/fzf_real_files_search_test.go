package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFForcedFilesSearchFromCD(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "forced files search")
	indexEnvironment := fixture.indexFixture(t, []indexedFixtureRecord{
		{path: []byte(filepath.Join(fixture.cwd, "first.txt"))},
		{path: []byte(filepath.Join(fixture.cwd, "second.txt"))},
	})
	term := fixture.startSized(t, protocol.PickerCD,
		indexEnvironment, 120, 35)
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
	waitForTerminalText(t, term, expectedIndexedSourceLabel()+" · files only")
	waitForTerminalText(t, term, "2/2")
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
		waitForTerminalTextAfter(t, term, before, "1/2")
		sendAndWait(t, term, keyCtrlSpace, barrier{Event: "callback.event", Operation: "mk", Count: i + 1})
	}
	// Browser round trip must preserve both marks and the files-only scope.
	before := len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 1})
	waitForTerminalTextAfter(t, term, before, "5/5")
	before = len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 2})
	waitForTerminalTextAfter(t, term, before, "1/2")
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
	indexEnvironment := fixture.indexFixture(t, nil)
	var indexPath string
	var indexVariables []string
	for _, entry := range indexEnvironment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			indexPath = value
		} else {
			indexVariables = append(indexVariables, entry)
		}
	}
	fixture.Release(t)
	environment := append([]string{
		"PATH=" + fixture.tools + string(os.PathListSeparator) + indexPath,
		parityHelperEnvironment + "=" + realZoxideHelperMode,
		realZoxideStartedEnvironment + "=" + fixture.started,
		realZoxideReleaseEnvironment + "=" + fixture.release,
		realZoxideRootEnvironment + "=" + fixture.home,
	}, indexVariables...)
	term := fixture.startSizedWithZoxideTimeout(t, protocol.PickerCD, environment, 120, 35, "0")
	defer term.Close()
	waitForTerminalText(t, term, "5/5")
	if err := term.Send([]byte("late-match")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "1/5")
	sendAndWait(t, term, keyCtrlSpace, barrier{Event: "callback.event", Operation: "mk", Count: 1})
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 1})
	waitForTerminalText(t, term, expectedIndexedSourceLabel()+" · directories only")
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
