package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type task5QueryCanarySinks struct {
	fzfCommandLine string
	descendants    []descendantProcessRecord
	trace          string
	callbackOutput string
	callbackStderr string
}

func validateTask5QueryCanarySinks(canary string, sinks task5QueryCanarySinks) error {
	if canary == "" {
		return errors.New("query canary is empty")
	}
	if strings.Contains(sinks.fzfCommandLine, canary) {
		return fmt.Errorf("fzf command line contains query canary: %q", sinks.fzfCommandLine)
	}
	for _, record := range sinks.descendants {
		if strings.Contains(record.CommandLine, canary) {
			return fmt.Errorf("descendant command line contains query canary: %q", record.CommandLine)
		}
	}
	for name, value := range map[string]string{
		"trace":           sinks.trace,
		"callback output": sinks.callbackOutput,
		"callback stderr": sinks.callbackStderr,
	} {
		if strings.Contains(value, canary) {
			return fmt.Errorf("%s contains query canary", name)
		}
	}
	return nil
}

const task5QueryCanary = "task5-query-canary"

var task5RecordingPickerCache realBinaryCache

func TestTask5QueryCanarySinkValidatorRejectsInjectedDescendantArgument(t *testing.T) {
	err := validateTask5QueryCanarySinks(task5QueryCanary, task5QueryCanarySinks{
		descendants: []descendantProcessRecord{{PID: 42, Identity: "42:start", CommandLine: "callback --arg " + task5QueryCanary}},
	})
	if err == nil {
		t.Fatal("query-canary sink validator accepted injected descendant argument")
	}
}

func TestRealFZFListenSidecarQueryCanaryDoesNotLeak(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "real sidecar query canary")
	callbackOutputPath, callbackStderrPath := filepath.Join(t.TempDir(), "callback.out"), filepath.Join(t.TempDir(), "callback.err")
	recordingPicker := buildTask5RecordingPicker(t)
	term := startTask5RecordingPicker(t, fixture, recordingPicker, []string{
		fzfsidecar.ActivationVariable + "=1",
		"SHELL_PICKER_TASK5_CALLBACK_OUTPUT=" + callbackOutputPath,
		"SHELL_PICKER_TASK5_CALLBACK_STDERR=" + callbackStderrPath,
	})
	defer term.Close()

	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	total := candidateCountForGeneration(t, term.TraceEvents(), 1)
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: 1})
	waitForCurrentListLabel(t, term, 0, fmt.Sprintf("%d/%d", total, total))
	beforeQuery := len(term.Output())
	if err := term.Send([]byte(task5QueryCanary)); err != nil {
		t.Fatal(err)
	}
	waitForCurrentListLabel(t, term, beforeQuery, fmt.Sprintf("0/%d", total))
	getSuccessBeforeQuery := traceCount(term.TraceEvents(), "sidecar.get", "success")
	term.WaitBarrier(testContext(t), barrier{Event: "sidecar.get", Operation: "success", Count: getSuccessBeforeQuery + 1})
	records := term.DescendantProcessRecords(t)
	callbackOutput := readTask5Capture(t, callbackOutputPath)
	callbackStderr := readTask5Capture(t, callbackStderrPath)
	if err := validateTask5QueryCanarySinks(task5QueryCanary, task5QueryCanarySinks{
		fzfCommandLine: term.FZFCommandLine(t), descendants: records,
		trace: fmt.Sprintf("%+v", term.TraceEvents()), callbackOutput: string(callbackOutput), callbackStderr: string(callbackStderr),
	}); err != nil {
		t.Fatal(err)
	}

	beforeClear := len(term.Output())
	if err := term.Send(bytes.Repeat([]byte{0x7f}, len(task5QueryCanary))); err != nil {
		t.Fatal(err)
	}
	waitForCurrentListLabel(t, term, beforeClear, fmt.Sprintf("%d/%d", total, total))
	if err := term.Send(keyEsc); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func buildTask5RecordingPicker(t *testing.T) string {
	t.Helper()
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := task5RecordingPickerCache.paths(func(root string) (string, string, error) {
		path := filepath.Join(root, binaryName("task5-recording-picker"))
		command := exec.Command("go", "build", "-o", path, "./integration/testhelper/recordingpicker")
		command.Dir = repository
		command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
		if output, err := command.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("build Task5 recording picker: %w\n%s", err, output)
		}
		return path, "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func startTask5RecordingPicker(t *testing.T, fixture *realFZFFixture, executable string, extraEnvironment []string) terminalSession {
	t.Helper()
	args := []string{string(protocol.PickerCP), "--cwd", fixture.cwd, "--home", fixture.home, "--fzf", fixture.fzf,
		"--zoxide-policy", "cached", "--zoxide-timeout", "5ms"}
	environment := replaceEnvironment(os.Environ(),
		"FZF_DEFAULT_OPTS=--bind=start:abort", "FZF_DEFAULT_COMMAND=printf forged",
		"SHELL_PICKER_ADDR=http://127.0.0.1:1", "SHELL_PICKER_TOKEN=forged", "TERM=xterm-256color")
	environment = replaceEnvironment(environment, extraEnvironment...)
	return newTerminalSession(t, terminalConfig{Path: executable, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: 120, Lines: 35})
}

func readTask5Capture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read Task5 callback capture %q: %v", path, err)
	}
	return data
}
