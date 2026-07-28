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
