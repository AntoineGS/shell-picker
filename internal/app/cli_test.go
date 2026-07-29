package app

import (
	"bytes"
	"context"
	"reflect"
	"testing"
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
