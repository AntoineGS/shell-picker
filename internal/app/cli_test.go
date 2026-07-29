package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"version"}, Streams{Out: &stdout, Err: &stderr}, "v1.2.3")
	if code != 0 || stdout.String() != "shell-picker v1.2.3\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPreviewEnvironmentAllowsOnlyPathLocaleAndTerminalMetadata(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin", "LANG=en_CA.UTF-8", "LC_ALL=C", "LC_CTYPE=C.UTF-8", "TERM=xterm-256color",
		"COLORTERM=truecolor", "NO_COLOR=1", "GITHUB_TOKEN=secret", "AWS_SECRET_ACCESS_KEY=secret",
		"SSH_AUTH_SOCK=/tmp/agent", "FZF_QUERY=event", "SHELL_PICKER_TOKEN=credential", "HOME=/home/user",
	}
	want := []string{"PATH=/usr/bin", "LANG=en_CA.UTF-8", "LC_ALL=C", "LC_CTYPE=C.UTF-8", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=1"}
	if got := previewEnvironment(inherited); !reflect.DeepEqual(got, want) {
		t.Fatalf("previewEnvironment()=%q want %q", got, want)
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
		"--fzf", "/bin/fzf", "--zoxide-policy", "fresh", "--zoxide-timeout", "0"}, "/bin/shell-picker")
	if err != nil {
		t.Fatal(err)
	}
	if overrides.Picker != protocol.PickerCP || overrides.Output != protocol.OutputNUON || overrides.FZFPath != "/bin/fzf" ||
		overrides.ZoxidePolicy != candidate.ZoxideFresh || overrides.ZoxideTimeout != 0 {
		t.Fatalf("overrides=%+v", overrides)
	}
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
		filepath.Join(cwd, "shell-picker"), dependencies)
	os.Exit(code)
}
