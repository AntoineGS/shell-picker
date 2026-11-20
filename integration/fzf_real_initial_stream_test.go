package integration

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFInitialUnearthSearchStreamsAndIgnoresEnterUntilLoad(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "initial index stream")
	extraEnvironment, readyPath, releasePath := fixture.delayedIndexFixture(t,
		[]byte(filepath.Join(fixture.cwd, "stream-hit.txt")), 0)
	term := fixture.startSized(t, protocol.PickerSearch, extraEnvironment, 120, 35)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	waitForTerminalText(t, term, "Loading index")
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte("stream ")); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte{0x7f}); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte("hit")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "stream-hit.txt")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(visibleTerminalOutput(term.Output()), []byte("callback failed")) {
		t.Fatalf("provisional preview/key callback was not suppressed: %q", visibleTerminalOutput(term.Output()))
	}
	// If Enter was not gated, fzf exits here before the source can become
	// authoritative. Holding the child makes this ordering deterministic.
	if err := waitForPath(t, releasePath, false, 80*time.Millisecond); err != nil {
		t.Fatalf("stream source unexpectedly released: %v", err)
	}
	beforeRelease := len(term.Output())
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeRelease, expectedIndexedSourceLabel())
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("wait for streamed selection: %v; events=%+v; output=%q",
			err, term.TraceEvents(), visibleTerminalOutput(term.Output()))
	}
	fixture.AssertAccepted(t, term, filepath.Join(fixture.cwd, "stream-hit.txt"))
}

func TestInitialUnearthRejectsSelectionWithoutFZFLoadAcknowledgment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "missing fzf load acknowledgment")
	indexEnvironment := fixture.indexFixture(t, []indexedFixtureRecord{
		{path: []byte(filepath.Join(fixture.cwd, "forged-selection.txt"))},
	})
	fakeFZF := filepath.Join(fixture.root, "fake-fzf")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '0.75.0\\n'; exit; fi\n" +
		"printf 'enter\\0'\nexec /bin/cat\n"
	if err := os.WriteFile(fakeFZF, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.fzf = fakeFZF
	term := fixture.startSized(t, protocol.PickerSearch, indexEnvironment, 120, 35)
	defer term.Close()
	if err := term.Wait(testContext(t)); err == nil {
		t.Fatal("picker accepted fzf output without a load acknowledgment")
	}
	if got := term.ResultBytes(); len(got) != 0 {
		t.Fatalf("unacknowledged selection wrote stdout: %q", got)
	}
}

func TestRealFZFInitialUnearthFailureCannotSelectVisibleProvisionalRow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "failed initial index stream")
	extraEnvironment, readyPath, releasePath := fixture.delayedIndexFixture(t,
		[]byte(filepath.Join(fixture.cwd, "failed-hit.txt")), 7)
	term := fixture.startSized(t, protocol.PickerSearch, extraEnvironment, 120, 35)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "failed-hit.txt")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := waitForPath(t, releasePath, false, 80*time.Millisecond); err != nil {
		t.Fatalf("stream source unexpectedly released: %v", err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err == nil {
		t.Fatalf("failed stream picker exited successfully; output=%q", visibleTerminalOutput(term.Output()))
	}
	if got := term.ResultBytes(); len(got) != 0 {
		t.Fatalf("partial source produced selected stdout %q (% x)", got, got)
	}
}

func TestRealFZFInitialUnearthMalformedTailCannotSelectVisibleProvisionalRow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "malformed initial index stream")
	extraEnvironment, readyPath, releasePath := fixture.malformedTailIndexFixture(t,
		[]byte(filepath.Join(fixture.cwd, "malformed-hit.txt")))
	term := fixture.startSized(t, protocol.PickerSearch, extraEnvironment, 120, 35)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "malformed-hit.txt")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := waitForPath(t, releasePath, false, 80*time.Millisecond); err != nil {
		t.Fatalf("stream source unexpectedly released: %v", err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err == nil {
		t.Fatalf("malformed stream picker exited successfully; output=%q", visibleTerminalOutput(term.Output()))
	}
	if got := term.ResultBytes(); len(got) != 0 {
		t.Fatalf("malformed source produced selected stdout %q (% x)", got, got)
	}
}

func TestRealFZFInitialUnearthEscapeBeforeFirstRowReapsSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "cancel before first indexed row")
	extraEnvironment, readyPath, _ := fixture.waitingIndexFixture(t)
	term := fixture.startSized(t, protocol.PickerSearch, extraEnvironment, 120, 35)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	waitForTerminalText(t, term, "Loading index")
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("wait for Escape cancellation: %v; events=%+v", err, term.TraceEvents())
	}
	term.(interface{ AssertNoLiveDescendants(*testing.T) }).AssertNoLiveDescendants(t)
	if got := term.ResultBytes(); len(got) != 0 {
		t.Fatalf("canceled stream produced stdout %q", got)
	}
}

func TestRealFZFInitialUnearthEscapeDuringBlockedTailReapsSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "cancel during initial index stream")
	extraEnvironment, readyPath, _ := fixture.delayedIndexFixture(t,
		[]byte(filepath.Join(fixture.cwd, "cancel-hit.txt")), 0)
	term := fixture.startSized(t, protocol.PickerSearch, extraEnvironment, 120, 35)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "cancel-hit.txt")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("wait for blocked-tail cancellation: %v; events=%+v",
			err, term.TraceEvents())
	}
	term.(interface{ AssertNoLiveDescendants(*testing.T) }).AssertNoLiveDescendants(t)
	if got := term.ResultBytes(); len(got) != 0 {
		t.Fatalf("canceled provisional stream produced stdout %q", got)
	}
}

func TestRealFZFInitialUnearthFilesOnlySearch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "seeded files-only index stream")
	file := []byte(filepath.Join(fixture.cwd, "seeded-hit.txt"))
	dir := []byte(filepath.Join(fixture.cwd, "seeded-dir") + string(os.PathSeparator))
	extraEnvironment, readyPath, releasePath := fixture.delayedIndexRecordsFixture(t, [][]byte{dir, file}, 0)
	args := []string{"search", "--cwd", fixture.cwd, "--home", fixture.home,
		"--search-scope", "files", "--fzf", fixture.fzf,
		"--zoxide-policy", "cached"}
	environment := replaceEnvironment(os.Environ(), "FZF_DEFAULT_OPTS=--bind=start:abort",
		"FZF_DEFAULT_COMMAND=printf forged", "TERM=xterm-256color")
	environment = replaceEnvironment(environment, extraEnvironment...)
	term := newTerminalSession(t, terminalConfig{Path: fixture.picker, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: 120, Lines: 35})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte("seeded-hit")); err != nil {
		t.Fatal(err)
	}
	waitForVisibleText(t, term, "seeded-hit.txt", 3*time.Second)
	visible := visibleTerminalOutput(term.Output())
	if bytes.Contains(visible, []byte("seeded-dir")) {
		t.Fatalf("files-only stream did not filter the directory: %q", visibleTerminalOutput(term.Output()))
	}
	beforeRelease := len(term.Output())
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeRelease, "Unearth · files only")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("wait for seeded files-only selection: %v; events=%+v",
			err, term.TraceEvents())
	}
	fixture.AssertAccepted(t, term, string(file))
}

func TestRealFZFInitialUnearthEmptySuccessLoadsNormally(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unearth is the Linux indexed source")
	}
	fixture := newRealFZFFixture(t, requireRealFZF(t), "empty initial index stream")
	extraEnvironment, readyPath, releasePath := fixture.delayedIndexRecordsFixture(t, nil, 0)
	term := fixture.startSized(t, protocol.PickerSearch, extraEnvironment, 120, 35)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	if err := waitForPath(t, readyPath, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	beforeRelease := len(term.Output())
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeRelease, expectedIndexedSourceLabel())
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("empty successful stream failed: %v; events=%+v",
			err, term.TraceEvents())
	}
	if got := term.ResultBytes(); len(got) != 0 {
		t.Fatalf("empty successful stream produced stdout %q", got)
	}
}

func (fixture *realFZFFixture) delayedIndexFixture(t *testing.T, path []byte, exitCode int) ([]string, string, string) {
	return fixture.delayedIndexRecordsFixture(t, [][]byte{path}, exitCode)
}

func (fixture *realFZFFixture) delayedIndexRecordsFixture(t *testing.T, paths [][]byte, exitCode int) ([]string, string, string) {
	t.Helper()
	bin := filepath.Join(fixture.root, "delayed-unearth-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(fixture.root, "first-record")
	var records bytes.Buffer
	for _, path := range paths {
		records.Write(recordFixtureBytes(path))
	}
	if err := os.WriteFile(firstPath, records.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(fixture.root, "source-ready")
	releasePath := filepath.Join(fixture.root, "source-release")
	script := "#!/bin/sh\n" +
		"/bin/cat \"$SHELL_PICKER_UNEARTH_FIRST\"\n" +
		": > \"$SHELL_PICKER_UNEARTH_READY\"\n" +
		"while [ ! -e \"$SHELL_PICKER_UNEARTH_RELEASE\" ]; do /bin/sleep 0.01; done\n" +
		fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(bin, "unearth"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SHELL_PICKER_UNEARTH_FIRST=" + firstPath,
		"SHELL_PICKER_UNEARTH_READY=" + readyPath,
		"SHELL_PICKER_UNEARTH_RELEASE=" + releasePath,
	}, readyPath, releasePath
}

func (fixture *realFZFFixture) waitingIndexFixture(t *testing.T) ([]string, string, string) {
	t.Helper()
	bin := filepath.Join(fixture.root, "waiting-unearth-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(fixture.root, "source-ready")
	releasePath := filepath.Join(fixture.root, "source-release")
	script := "#!/bin/sh\n" +
		": > \"$SHELL_PICKER_UNEARTH_READY\"\n" +
		"while [ ! -e \"$SHELL_PICKER_UNEARTH_RELEASE\" ]; do /bin/sleep 0.01; done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "unearth"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SHELL_PICKER_UNEARTH_READY=" + readyPath,
		"SHELL_PICKER_UNEARTH_RELEASE=" + releasePath,
	}, readyPath, releasePath
}

func (fixture *realFZFFixture) malformedTailIndexFixture(t *testing.T, path []byte) ([]string, string, string) {
	t.Helper()
	bin := filepath.Join(fixture.root, "malformed-unearth-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(fixture.root, "valid-record")
	if err := os.WriteFile(validPath, recordFixtureBytes(path), 0o600); err != nil {
		t.Fatal(err)
	}
	tailPath := filepath.Join(fixture.root, "malformed-tail")
	if err := os.WriteFile(tailPath, []byte{1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(fixture.root, "source-ready")
	releasePath := filepath.Join(fixture.root, "source-release")
	script := "#!/bin/sh\n" +
		"/bin/cat \"$SHELL_PICKER_UNEARTH_VALID\"\n" +
		": > \"$SHELL_PICKER_UNEARTH_READY\"\n" +
		"while [ ! -e \"$SHELL_PICKER_UNEARTH_RELEASE\" ]; do /bin/sleep 0.01; done\n" +
		"/bin/cat \"$SHELL_PICKER_UNEARTH_TAIL\"\n"
	if err := os.WriteFile(filepath.Join(bin, "unearth"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SHELL_PICKER_UNEARTH_VALID=" + validPath,
		"SHELL_PICKER_UNEARTH_TAIL=" + tailPath,
		"SHELL_PICKER_UNEARTH_READY=" + readyPath,
		"SHELL_PICKER_UNEARTH_RELEASE=" + releasePath,
	}, readyPath, releasePath
}

func recordFixtureBytes(path []byte) []byte {
	var stream bytes.Buffer
	_ = binary.Write(&stream, binary.LittleEndian, uint32(len(path)))
	_, _ = stream.Write(path)
	return stream.Bytes()
}

func waitForPath(t *testing.T, path string, exists bool, timeout time.Duration) error {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := os.Stat(path)
		if exists && err == nil {
			return nil
		}
		if !exists && os.IsNotExist(err) {
			select {
			case <-ticker.C:
				continue
			case <-deadline.C:
				return nil
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			if exists {
				return fmt.Errorf("timed out waiting for %s", path)
			}
			return fmt.Errorf("%s appeared before timeout", path)
		}
	}
}

func waitForVisibleText(t *testing.T, term terminalSession, text string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains(visibleTerminalOutput(term.Output()), []byte(text)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal did not show %q: output=%q; events=%+v", text,
		visibleTerminalOutput(term.Output()), term.TraceEvents())
}
