package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"version"}, Streams{Out: &stdout, Err: &stderr}, "v1.2.3")
	if code != 0 || stdout.String() != "shell-picker v1.2.3\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestVersionCommandUsesDefaultWhenBuildVersionIsEmpty(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"version"}, Streams{Out: &stdout, Err: &stderr}, "")
	if code != 0 || stdout.String() != "shell-picker dev\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestProbeCLIEmitsDeterministicJSONWithoutRawDependencyPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runProbeCLI(context.Background(), []string{"probe", "--json"}, Streams{Out: &stdout, Err: &stderr}, "v1.2.3",
		integrationpkg.ProbeOptions{
			FZFPath: "fzf", ZoxidePath: "zoxide", PreviewTools: []string{"bat"},
			LookupPath: func(name string) (string, error) {
				if name == "fzf" {
					return "/private/bin/fzf", nil
				}
				return "", os.ErrNotExist
			},
			CheckFZF: func(context.Context, string, []string) (string, error) { return "0.74.1", nil },
			Cache:    integrationpkg.ProbeCache{Root: "/private/cache", Status: "writable"},
		})
	if code != 0 || stderr.Len() != 0 || !bytes.HasSuffix(stdout.Bytes(), []byte("\n")) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var report integrationpkg.ProbeReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.FZF.Version != "0.74.1" || report.Zoxide.Status != "optional-missing" {
		t.Fatalf("report=%+v", report)
	}
	for _, forbidden := range []string{"/private/bin/fzf", "/private/cache", "token", "query", "record"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("probe leaked %q: %s", forbidden, stdout.String())
		}
	}

	stdout.Reset()
	if code := runProbeCLI(context.Background(), []string{"probe"}, Streams{Out: &stdout, Err: &stderr}, "dev", integrationpkg.ProbeOptions{}); code != 2 {
		t.Fatalf("probe without --json code=%d", code)
	}
}

func TestPreviewEnvironmentAllowsOnlyPathLocaleAndTerminalMetadata(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin", "LANG=en_CA.UTF-8", "LC_ALL=C", "LC_CTYPE=C.UTF-8", "TERM=xterm-256color",
		"COLORTERM=truecolor", "NO_COLOR=1", "GITHUB_TOKEN=secret", "AWS_SECRET_ACCESS_KEY=secret",
		"SSH_AUTH_SOCK=/tmp/agent", "FZF_QUERY=event", "FZF_PREVIEW_COLUMNS=999", "FZF_PREVIEW_LINES=888",
		"SHELL_PICKER_TOKEN=credential", "HOME=/home/user",
	}
	want := []string{"PATH=/usr/bin", "LANG=en_CA.UTF-8", "LC_ALL=C", "LC_CTYPE=C.UTF-8", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=1"}
	if got := previewEnvironment(inherited); !reflect.DeepEqual(got, want) {
		t.Fatalf("previewEnvironment()=%q want %q", got, want)
	}
}

func TestPreviewDimensionsNormalizeCallbackEnvironment(t *testing.T) {
	tests := []struct {
		name, columns, lines   string
		wantColumns, wantLines int
	}{
		{name: "defaults", wantColumns: 80, wantLines: 40},
		{name: "valid", columns: "132", lines: "57", wantColumns: 132, wantLines: 57},
		{name: "malformed", columns: "12x", lines: "-4", wantColumns: 80, wantLines: 40},
		{name: "zero and overflow", columns: "0", lines: "999999999999999999999999", wantColumns: 80, wantLines: 40},
		{name: "clamped", columns: "1001", lines: "5000", wantColumns: 1000, wantLines: 1000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"FZF_PREVIEW_COLUMNS": tc.columns, "FZF_PREVIEW_LINES": tc.lines}
			columns, lines := previewDimensions(func(name string) string { return values[name] })
			if columns != tc.wantColumns || lines != tc.wantLines {
				t.Fatalf("dimensions=%dx%d want %dx%d", columns, lines, tc.wantColumns, tc.wantLines)
			}
		})
	}
}

func TestPreviewCallbackPassesClampedDimensionsWithoutChildEnvLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixture is Unix-specific")
	}
	path := filepath.Join(t.TempDir(), "readme.md")
	if err := os.WriteFile(path, []byte("# title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		var input sessionipc.PreviewRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			http.Error(response, "bad request", http.StatusBadRequest)
			return
		}
		if input.Phase == "resolve" {
			_ = json.NewEncoder(response).Encode(sessionipc.PreviewResponse{Kind: protocol.KindFile,
				PathBase64: base64.StdEncoding.EncodeToString([]byte(path))})
			return
		}
		_, _ = response.Write([]byte("{}"))
	}))
	defer server.Close()
	tools, log := t.TempDir(), filepath.Join(t.TempDir(), "glow.log")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf 'dims=%%s,%%s\\n' \"$FZF_PREVIEW_COLUMNS\" \"$FZF_PREVIEW_LINES\" >> %q\nprintf rendered\n", log, log)
	if err := os.WriteFile(filepath.Join(tools, "glow"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL_PICKER_ADDR", server.URL)
	t.Setenv("SHELL_PICKER_TOKEN", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	t.Setenv("FZF_CURRENT_ITEM", "record")
	t.Setenv("FZF_PREVIEW_COLUMNS", "5000")
	t.Setenv("FZF_PREVIEW_LINES", "57")
	t.Setenv("PATH", tools)
	var stdout, stderr bytes.Buffer
	if code := callbackMain(context.Background(), []string{"--fzf-shell", "p"}, Streams{Out: &stdout, Err: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "--width\n999\n"+path+"\ndims=,\n" {
		t.Fatalf("log=%q err=%v", data, err)
	}
}

func TestFZFShellRejectsMissingOrExtraCommandText(t *testing.T) {
	for _, args := range [][]string{{"--fzf-shell"}, {"--fzf-shell", "p", "extra"}, {"--fzf-shell", "$(id)"}} {
		var stdout, stderr bytes.Buffer
		code := Main(context.Background(), args, Streams{Out: &stdout, Err: &stderr}, "dev")
		if code != 2 || stdout.Len() != 0 {
			t.Fatalf("args=%q code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if bytes.Contains(stderr.Bytes(), []byte("SHELL_PICKER_TOKEN")) {
			t.Fatalf("secret variable advertised: %q", stderr.String())
		}
	}
}

func TestFZFShellInfoDoesNotRequireIPCCredentials(t *testing.T) {
	t.Setenv("SHELL_PICKER_ADDR", "")
	t.Setenv("SHELL_PICKER_TOKEN", "")
	t.Setenv("FZF_MATCH_COUNT", "7")
	t.Setenv("FZF_TOTAL_COUNT", "42")
	t.Setenv("FZF_SELECT_COUNT", "1")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--fzf-shell", "i:cp"}, Streams{Out: &stdout, Err: &stderr}, "dev")
	if code != 0 || stdout.String() != "7/42 (1)" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestFixedCallbacksNeedNoIPCCredentials(t *testing.T) {
	t.Setenv("SHELL_PICKER_ADDR", "")
	t.Setenv("SHELL_PICKER_TOKEN", "")
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "l:empty", want: ""},
		{command: "p:invalid", want: "[Invalid Path]"},
	} {
		t.Run(test.command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := callbackMain(context.Background(), []string{"--fzf-shell", test.command}, Streams{Out: &stdout, Err: &stderr})
			if code != 0 || stdout.String() != test.want || stderr.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestFZFShellTransportFailureReturnsOneWithoutCredentials(t *testing.T) {
	t.Setenv("SHELL_PICKER_ADDR", "http://127.0.0.1:1")
	t.Setenv("SHELL_PICKER_TOKEN", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("FZF_CURRENT_ITEM", "item")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--fzf-shell", "p"}, Streams{Out: &stdout, Err: &stderr}, "dev")
	if code != 1 || bytes.Contains(stderr.Bytes(), []byte("0123456789abcdef")) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestFZFShellRejectsEventKeyBeforeIPCClientConstruction(t *testing.T) {
	t.Setenv("SHELL_PICKER_ADDR", "")
	t.Setenv("SHELL_PICKER_TOKEN", "")
	t.Setenv("FZF_KEY", "$(id)")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--fzf-shell", "e:en"}, Streams{Out: &stdout, Err: &stderr}, "dev")
	if code != 2 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPickerCLIParsesDefaultsOverridesAndExplicitZero(t *testing.T) {
	defaults, err := parsePickerArgs([]string{"cd", "--cwd", "/work", "--home", "/home/u"}, "/bin/shell-picker")
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Picker != protocol.PickerCD || defaults.Output != protocol.OutputNUL || defaults.FZFPath != "fzf" ||
		defaults.ZoxidePolicy != candidate.ZoxideCached || defaults.ZoxideTimeout != candidate.DefaultZoxideTimeout() {
		t.Fatalf("defaults=%+v", defaults)
	}
	overrides, err := parsePickerArgs([]string{"cp", "--cwd", "/work", "--home", "/home/u", "--output", "nuon",
		"--fzf", "/bin/fzf", "--zoxide-policy", "fresh", "--zoxide-timeout", "0", "--trace", "/tmp/session.jsonl"}, "/bin/shell-picker")
	if err != nil {
		t.Fatal(err)
	}
	if overrides.Picker != protocol.PickerCP || overrides.Output != protocol.OutputNUON || overrides.FZFPath != "/bin/fzf" ||
		overrides.ZoxidePolicy != candidate.ZoxideFresh || overrides.ZoxideTimeout != 0 || overrides.TracePath != "/tmp/session.jsonl" {
		t.Fatalf("overrides=%+v", overrides)
	}
}

func TestPickerTraceCreatesPrivateFileAndRecordsLifecycle(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.options.TracePath = filepath.Join(t.TempDir(), "picker.trace.jsonl")
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		for _, value := range append(append([]string(nil), config.Environment...), config.Options...) {
			if strings.Contains(value, fixture.options.TracePath) {
				t.Fatalf("trace path inherited by fzf/callback: %q", value)
			}
		}
		config.Runner.Observe(process.ProcessEvent{Phase: "start", PID: 42, Path: config.FZFPath})
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fixture.options.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("trace mode=%#o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(fixture.options.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var names []string
	for decoder.More() {
		var event integrationpkg.TraceRecord
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		names = append(names, event.Event)
	}
	want := []string{"session.start", "generation.start", "generation.publish", "fzf.start", "fzf.exit", "session.close"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("trace events=%q want %q; raw=%s", names, want, raw)
	}
	if bytes.Contains(raw, []byte(fixture.cwd)) || bytes.Contains(raw, []byte("SHELL_PICKER_TOKEN")) ||
		bytes.Contains(raw, []byte("query")) || bytes.Contains(raw, []byte("record")) {
		t.Fatalf("trace leaked sensitive value: %s", raw)
	}
	var publication integrationpkg.TraceRecord
	decoder = json.NewDecoder(bytes.NewReader(raw))
	for decoder.More() {
		var event integrationpkg.TraceRecord
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "generation.publish" {
			publication = event
		}
	}
	if publication.ZoxidePolicy != "cached" || publication.ZoxideAttempts != 0 || publication.ZoxideStarts != 0 ||
		publication.ZoxideMaxLive != 0 || publication.ZoxideOutcome != "not-run" || publication.LocalUS < 0 || publication.TransformUS < 0 {
		t.Fatalf("publication=%+v", publication)
	}
}

func TestPickerTraceWriteFailureReportsOnceThenRemainsSecondary(t *testing.T) {
	var diagnostic bytes.Buffer
	writer := &appFailingTraceWriter{}
	sink := &pickerTrace{trace: integrationpkg.NewTrace(writer, [16]byte{1}), diagnostic: &diagnostic}
	sink.event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"})
	sink.event(integrationpkg.TraceEvent{Name: "session.close", Outcome: "error"})
	if writer.calls != 1 || diagnostic.String() != "shell-picker: trace disabled\n" {
		t.Fatalf("calls=%d diagnostic=%q", writer.calls, diagnostic.String())
	}
}

func TestPickerTraceFinishKeepsOutcomeWhenCloseFails(t *testing.T) {
	var diagnostic bytes.Buffer
	sink := &failingTraceSink{closeErr: errors.New("close failed")}
	trace := &pickerTrace{trace: integrationpkg.NewTrace(sink, [16]byte{1}), sink: sink, diagnostic: &diagnostic}
	want := protocol.Outcome{Status: protocol.StatusAccepted, Paths: [][]byte{[]byte("accepted")}}

	got, err := trace.finish(want, nil)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("finish outcome=%+v err=%v want=%+v", got, err, want)
	}
	if diagnostic.String() != "shell-picker: trace disabled\n" {
		t.Fatalf("diagnostic=%q", diagnostic.String())
	}
	if !bytes.Contains(sink.Bytes(), []byte(`"event":"session.close"`)) ||
		!bytes.Contains(sink.Bytes(), []byte(`"outcome":"accepted"`)) {
		t.Fatalf("trace=%s", sink.Bytes())
	}
}

func TestPickerTraceFinishKeepsPrimaryErrorWhenSessionCloseWriteFails(t *testing.T) {
	var diagnostic bytes.Buffer
	sink := &failingTraceSink{writeErr: errors.New("write failed")}
	trace := &pickerTrace{trace: integrationpkg.NewTrace(sink, [16]byte{1}), sink: sink, diagnostic: &diagnostic}
	primary := errors.New("picker failed")

	got, err := trace.finish(protocol.Outcome{}, primary)
	if !reflect.DeepEqual(got, protocol.Outcome{}) || !errors.Is(err, primary) {
		t.Fatalf("finish outcome=%+v err=%v", got, err)
	}
	if sink.closeCalls != 1 || diagnostic.String() != "shell-picker: trace disabled\n" {
		t.Fatalf("close calls=%d diagnostic=%q", sink.closeCalls, diagnostic.String())
	}
}

type failingTraceSink struct {
	bytes.Buffer
	writeErr   error
	closeErr   error
	closeCalls int
}

func (sink *failingTraceSink) Write(data []byte) (int, error) {
	if sink.writeErr != nil {
		return 0, sink.writeErr
	}
	return sink.Buffer.Write(data)
}

func (sink *failingTraceSink) Close() error {
	sink.closeCalls++
	return sink.closeErr
}

type appFailingTraceWriter struct{ calls int }

func (writer *appFailingTraceWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, errors.New("fixture write failure")
}

func TestPickerCLIRejectsInvalidAndDuplicateFlags(t *testing.T) {
	base := []string{"cd", "--cwd", "/work", "--home", "/home/u"}
	tests := [][]string{
		append(append([]string{}, base...), "--zoxide-policy", "stale"),
		append(append([]string{}, base...), "--zoxide-policy", "cached", "--zoxide-policy", "fresh"),
		append(append([]string{}, base...), "--zoxide-timeout", "-1ms"),
		append(append([]string{}, base...), "--zoxide-timeout", "forever"),
		append(append([]string{}, base...), "--cwd", "/other"),
		append(append([]string{}, base...), "extra"),
		{"cd", "--cwd", "relative", "--home", "/home/u"},
	}
	for _, args := range tests {
		if _, err := parsePickerArgs(args, "/bin/shell-picker"); err == nil {
			t.Errorf("parsePickerArgs(%q) succeeded", args)
		}
	}
}

func TestPickerCLITimeoutOverride(t *testing.T) {
	options, err := parsePickerArgs([]string{"cd", "--cwd", "/work", "--home", "/home/u", "--zoxide-timeout", "275ms"}, "/bin/shell-picker")
	if err != nil || options.ZoxideTimeout != 275*time.Millisecond {
		t.Fatalf("options=%+v err=%v", options, err)
	}
}

func TestPickerCLIProcessOutputAbortFailureAndDuplicateFlags(t *testing.T) {
	if os.Getenv("GO_WANT_PICKER_CLI_HELPER") == "1" {
		t.Skip("parent-only test")
	}
	tests := []struct {
		name, mode, output string
		args               []string
		code               int
	}{
		{"nul accepted", "accept", "readme.md\x00", []string{"cp", "--output", "nul"}, 0},
		{"nuon accepted", "accept", "{\"status\":\"accepted\",\"paths\":[\"readme.md\"]}\n", []string{"cp", "--output", "nuon"}, 0},
		{"nul abort", "abort", "", []string{"cp", "--output", "nul"}, 0},
		{"operational failure", "error", "", []string{"cp", "--output", "nul"}, 1},
		{"duplicate flag", "accept", "", []string{"cp", "--output", "nul", "--output", "nuon"}, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			if err := os.WriteFile(filepath.Join(cwd, "readme.md"), []byte("title\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-test.run=^TestPickerCLIProcessHelper$", "--"}
			args = append(args, test.args...)
			args = append(args, "--cwd", cwd, "--home", cwd)
			command := exec.Command(os.Args[0], args...)
			command.Env = append(os.Environ(), "GO_WANT_PICKER_CLI_HELPER=1", "GO_PICKER_MODE="+test.mode, "GO_PICKER_CWD="+cwd)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			code := 0
			if exit := new(exec.ExitError); errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != test.code || stdout.String() != test.output {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestPickerCLIProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PICKER_CLI_HELPER") != "1" {
		return
	}
	cwd := os.Getenv("GO_PICKER_CWD")
	terminal, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		os.Exit(99)
	}
	defer terminal.Close()
	dependencies := Dependencies{ForegroundTTY: terminal, ZoxidePath: filepath.Join(cwd, "missing-zoxide")}
	dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		switch os.Getenv("GO_PICKER_MODE") {
		case "accept":
			return fzf.Result{Key: "enter", Records: [][]byte{recordForPath(t, config.Input, filepath.Join(cwd, "readme.md"))}}, nil
		case "abort":
			return fzf.Result{Aborted: true, ExitCode: 130}, nil
		default:
			return fzf.Result{}, errors.New("fixture launch failure")
		}
	}
	separator := 0
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index + 1
			break
		}
	}
	code := runPickerCLI(context.Background(), os.Args[separator:], Streams{Out: os.Stdout, Err: os.Stderr},
		filepath.Join(cwd, "shell-picker"), &dependencies)
	os.Exit(code)
}
