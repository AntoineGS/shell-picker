package app

import (
	"bytes"
	"context"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"version"}, Streams{Out: &stdout, Err: &stderr}, "v1.2.3")
	if code != 0 || stdout.String() != "shell-picker v1.2.3\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
