package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var keyTab = []byte{'\t'}

func TestRealFZFListenSidecarCDLabelMatchesFZFState(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar cd label")
	term := fixture.start(t, protocol.PickerCD, []string{fzfsidecar.ActivationVariable + "=1"})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	total := candidateCountForGeneration(t, term.TraceEvents(), 1)
	waitForCurrentListLabel(t, term, 0, fmt.Sprintf("%d/%d", total, total))
}

func TestRealFZFListenSidecarUsesStaticHeaderAndNativeResize(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar header")
	term := fixture.start(t, protocol.PickerCP, []string{
		fzfsidecar.ActivationVariable + "=1",
		"FZF_API_KEY=task5-api-canary",
		"SHELL_PICKER_TASK5_STATE_CANARY=task5-state-canary",
	})
	defer term.Close()

	beforeStart := len(term.Output())
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	waitForCurrentScreenTextAfter(t, term, beforeStart, filepath.Base(fixture.cwd))
	if got := traceCount(term.TraceEvents(), "preview.finished", ""); got != 0 {
		t.Fatalf("preview.finished count=%d before header assertion, want zero", got)
	}
	if got := traceCount(term.TraceEvents(), "callback.display", ""); got != 0 {
		t.Fatalf("enabled session callback.display count=%d at initial header, want zero", got)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "sidecar.post", Operation: "success", Count: 1})
	assertRealSidecarObserverTrace(t, term)
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: 1})
	assertRealFZFListenProcessEvidence(t, term)
	if got := traceCount(term.TraceEvents(), "callback.display", ""); got != 0 {
		t.Fatalf("enabled session callback.display count=%d, want zero before resize", got)
	}
	beforeResize := len(term.Output())
	if err := term.Resize(80, 35); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeResize, filepath.Base(fixture.cwd))
	if got := traceCount(term.TraceEvents(), "callback.display", ""); got != 0 {
		t.Fatalf("enabled session callback.display count=%d after resize, want zero", got)
	}
}

func TestRealFZFListenSidecarProcessTopology(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar topology")
	term := fixture.start(t, protocol.PickerCP, []string{fzfsidecar.ActivationVariable + "=1"})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
}

func assertRealFZFListenProcessEvidence(t *testing.T, term terminalSession) {
	t.Helper()
	command := term.FZFCommandLine(t)
	if !strings.Contains(command, "--listen=127.0.0.1:") {
		t.Fatalf("enabled fzf command line lacks numeric listen address: %q", command)
	}
	for _, forbidden := range []string{"--info-command=i:cd", "--info-command=i:cp", "task5-api-canary", "task5-state-canary", "task5-query-canary"} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("fzf command line contains forbidden %q: %q", forbidden, command)
		}
	}
	if strings.Contains(command, "transform(d)") {
		t.Fatalf("enabled fzf command line invokes display: %q", command)
	}
	records := term.DescendantProcessRecords(t)
	if len(records) == 0 {
		t.Fatal("process spy observed no picker descendants")
	}
	fzfCommand := term.FZFCommandLine(t)
	sawFZF, sawCallback := false, false
	for _, record := range records {
		if record.Identity == "" || record.CommandLine == "" {
			t.Fatalf("process recorder omitted identity or command line: %+v", record)
		}
		if record.CommandLine == fzfCommand {
			sawFZF = true
		}
		lowerCommand := strings.ToLower(record.CommandLine)
		if strings.Contains(lowerCommand, "--fzf-shell") {
			sawCallback = true
		}
		observed := record.CommandLine
		for _, forbidden := range []string{"--info-command=i:cd", "--info-command=i:cp", "transform(d)", "task5-api-canary", "task5-state-canary", "task5-query-canary"} {
			if strings.Contains(observed, forbidden) {
				t.Fatalf("descendant command line contains forbidden %q: %q", forbidden, observed)
			}
		}
	}
	if !sawFZF || !sawCallback {
		t.Fatalf("process recorder evidence fzf/callback=%t/%t; records=%+v", sawFZF, sawCallback, records)
	}
	traceText := fmt.Sprintf("%+v", term.TraceEvents())
	sinks := []string{string(term.Output()), string(term.ResultBytes()), traceText}
	for _, sink := range sinks {
		for _, forbidden := range []string{"task5-api-canary", "task5-state-canary", "task5-query-canary", "FZF_API_KEY=", "SHELL_PICKER_TOKEN="} {
			if strings.Contains(sink, forbidden) {
				t.Fatalf("sink contains forbidden %q: %q", forbidden, sink)
			}
		}
	}
}

func assertRealSidecarObserverTrace(t *testing.T, term terminalSession) {
	t.Helper()
	events := term.TraceEvents()
	if traceCount(events, "sidecar.get", "success") == 0 || traceCount(events, "sidecar.post", "success") == 0 {
		t.Fatalf("sidecar observer emitted no successful GET/POST diagnostics: %s; events=%+v", sidecarDiagnostics(events), events)
	}
	for _, event := range events {
		if !strings.HasPrefix(event.Event, "sidecar.") {
			continue
		}
		if event.Generation != 0 || event.CandidateCount != 0 || event.Renderer != "" || event.Path != "" {
			t.Fatalf("sidecar observer carried non-diagnostic fields: %+v", event)
		}
		if (event.Event == "sidecar.get" || event.Event == "sidecar.post") && event.SidecarAttempt == 0 {
			t.Fatalf("sidecar operation lacks attempt counter: %+v", event)
		}
	}
}

func TestRealFZFListenSidecarCDTabSelectionPersistsAcrossFilter(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar cd tab")
	term := fixture.start(t, protocol.PickerCD, []string{fzfsidecar.ActivationVariable + "=1"})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	total := candidateCountForGeneration(t, term.TraceEvents(), 1)
	waitForCurrentListLabel(t, term, 0, fmt.Sprintf("%d/%d", total, total))
	previewBefore := traceCount(term.TraceEvents(), "preview.finished", "")
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBefore + 1})
	beforeMove := len(term.Output())
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeMove)
	beforeTab := len(term.Output())
	if err := term.Send(keyTab); err != nil {
		t.Fatal(err)
	}
	waitForCurrentListLabel(t, term, beforeTab, fmt.Sprintf("%d/%d", total, total))
	getSuccessBeforeQuery := traceCount(term.TraceEvents(), "sidecar.get", "success")
	term.WaitBarrier(testContext(t), barrier{Event: "sidecar.get", Operation: "success", Count: getSuccessBeforeQuery + 1})
	beforeQuery := len(term.Output())
	previewBeforeQuery := traceCount(term.TraceEvents(), "preview.finished", "")
	if err := term.Send([]byte("visible")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "visible")
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBeforeQuery + 1})
	waitForCurrentListLabel(t, term, beforeQuery, fmt.Sprintf("1/%d", total))
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, filepath.Join(fixture.cwd, "alpha"))
}

func TestRealFZFListenSidecarDoesNotLeakCredentialToPreviewRenderer(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar credential")
	tools := t.TempDir()
	eza := filepath.Join(tools, binaryName("eza"))
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", eza, "./integration/testhelper/resource")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build environment renderer: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(tools, "block"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := []string{fzfsidecar.ActivationVariable + "=1", "PATH=" + tools + string(os.PathListSeparator) + os.Getenv("PATH")}
	term := fixture.start(t, protocol.PickerCP, environment)
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	logPath := filepath.Join(tools, "environment.log")
	data, err := waitForRealZoxideFile(testContext(t), logPath)
	if err != nil {
		t.Fatalf("wait for preview renderer environment: %v", err)
	}
	assertRealFZFListenPreviewProcessEvidence(t, term)
	for _, forbidden := range []string{"FZF_API_KEY=", "SHELL_PICKER_ADDR=", "SHELL_PICKER_TOKEN=", fzfsidecar.ActivationVariable + "="} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("preview renderer environment leaked %q: %q", forbidden, data)
		}
	}
	output := string(term.Output())
	if strings.Contains(output, "FZF_API_KEY=") || strings.Contains(output, "SHELL_PICKER_TOKEN=") {
		t.Fatalf("sidecar credential/state reached terminal output: %q", output)
	}
}

func assertRealFZFListenPreviewProcessEvidence(t *testing.T, term terminalSession) {
	t.Helper()
	records := term.DescendantProcessRecords(t)
	sawCallback, sawRenderer := false, false
	for _, record := range records {
		lowerCommand := strings.ToLower(record.CommandLine)
		sawCallback = sawCallback || strings.Contains(lowerCommand, "--fzf-shell")
		sawRenderer = sawRenderer || strings.Contains(lowerCommand, "eza")
	}
	if !sawCallback || !sawRenderer {
		t.Fatalf("preview process recorder evidence callback/renderer=%t/%t; records=%+v", sawCallback, sawRenderer, records)
	}
}

func TestRealFZFListenSidecarCPLabelTracksQueryFilteringAndAccept(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar query")
	term := fixture.start(t, protocol.PickerCP, []string{fzfsidecar.ActivationVariable + "=1"})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	previewBefore := traceCount(term.TraceEvents(), "preview.finished", "")
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBefore + 1})
	total := candidateCountForGeneration(t, term.TraceEvents(), 1)
	waitForCurrentListLabel(t, term, 0, fmt.Sprintf("%d/%d", total, total))
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "alpha")
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBefore + 2})
	waitForCurrentListLabel(t, term, beforeQuery, fmt.Sprintf("1/%d", total))

	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "alpha")
}

func TestRealFZFListenSidecarCDTracksLateZoxideFiltering(t *testing.T) {
	fixture := newRealZoxideFixture(t, requireRealFZF(t))
	environment := replaceEnvironment(os.Environ(),
		"FZF_DEFAULT_OPTS=--bind=start:abort", "FZF_DEFAULT_COMMAND=printf forged",
		"SHELL_PICKER_ADDR=http://127.0.0.1:1", "SHELL_PICKER_TOKEN=forged", "TERM=xterm-256color",
		"PATH="+fixture.tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		parityHelperEnvironment+"="+realZoxideHelperMode,
		realZoxideStartedEnvironment+"="+fixture.started,
		realZoxideReleaseEnvironment+"="+fixture.release,
		realZoxideRootEnvironment+"="+fixture.home,
		fzfsidecar.ActivationVariable+"=1",
	)
	term := newTerminalSession(t, terminalConfig{
		Path: fixture.picker,
		Args: []string{string(protocol.PickerCD), "--cwd", fixture.cwd, "--home", fixture.home,
			"--fzf", fixture.fzf, "--zoxide-policy", "cached", "--zoxide-timeout", "0"},
		Environment: environment, Directory: fixture.cwd, Columns: 120, Lines: 35,
	})
	defer term.Close()

	initial := term.WaitBarrier(testContext(t), barrier{Event: "generation.publish", Generation: 1, Count: 1})
	assertRealZoxidePendingPublication(t, initial)
	initialTotal := initial.CandidateCount
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	waitForTerminalText(t, term, "local-visible")
	if _, err := waitForRealZoxideFile(testContext(t), fixture.started); err != nil {
		t.Fatal(err)
	}
	waitForCurrentListLabel(t, term, 0, fmt.Sprintf("%d/%d", initialTotal, initialTotal))

	beforeQuery := len(term.Output())
	if err := term.Send([]byte("late-match")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "late-match")
	waitForCurrentListLabel(t, term, beforeQuery, fmt.Sprintf("0/%d", initialTotal))
	fixture.Release(t)
	enrichment := term.WaitBarrier(testContext(t), barrier{Event: "zoxide.enrichment", Operation: "published", Count: 1})
	waitForTerminalText(t, term, filepath.Base(fixture.lateTarget))
	waitForCurrentListLabel(t, term, beforeQuery, fmt.Sprintf("1/%d", enrichment.CandidateCount))
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	assertRealZoxideAccepted(t, term, fixture.lateTarget)
}

func TestRealFZFListenSidecarCPLabelTracksTabSelectionAndAccept(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar cp label")
	term := fixture.start(t, protocol.PickerCP, []string{fzfsidecar.ActivationVariable + "=1"})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	previewBefore := traceCount(term.TraceEvents(), "preview.finished", "")
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBefore + 1})
	total := candidateCountForGeneration(t, term.TraceEvents(), 1)
	waitForCurrentListLabel(t, term, 0, fmt.Sprintf("%d/%d", total, total))
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "alpha")
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBefore + 2})
	waitForCurrentListLabel(t, term, beforeQuery, fmt.Sprintf("1/%d", total))
	beforeTab := len(term.Output())
	if err := term.Send(keyTab); err != nil {
		t.Fatal(err)
	}
	waitForCurrentListLabel(t, term, beforeTab, fmt.Sprintf("1/%d (1)", total))

	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "alpha")
}

func TestRealFZFListenSidecarAbortPreservesPickerOutcome(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar abort")
	term := fixture.start(t, protocol.PickerCP, []string{fzfsidecar.ActivationVariable + "=1"})
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	closed := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	if closed.Outcome != "aborted" || len(term.ResultBytes()) != 0 {
		t.Fatalf("sidecar abort event/result=%+v/%q", closed, term.ResultBytes())
	}
}

func waitForCurrentListLabel(t *testing.T, term terminalSession, before int, label string) {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if before < len(output) {
			if got, ok := currentListBorderLabel(output); ok && got == label {
				return
			}
		}
		select {
		case <-ctx.Done():
			got, ok := currentListBorderLabel(output)
			events := term.TraceEvents()
			t.Fatalf("current list label=%q/%t, want %q: %v; sidecar=%s; events=%+v", got, ok, label, ctx.Err(), sidecarDiagnostics(events), events)
		default:
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}

func sidecarDiagnostics(events []traceEvent) string {
	parts := make([]string, 0)
	for _, event := range events {
		if strings.HasPrefix(event.Event, "sidecar.") {
			parts = append(parts, fmt.Sprintf("%s/%s#%d/%dus", event.Event, event.Outcome, event.SidecarAttempt, event.LocalUS))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}
