package integration

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/process"
)

func TestRealFZFExactNULPipe(t *testing.T) {
	path := requireRealFZF(t)
	want := []byte("file\tdisplay\\twith space\tpayload-line-1\npayload-line-2\x00")
	input := append([]byte("file\tordinary\tother\x00"), want...)
	input = append(input, []byte("file\tlast record\tother-last\x00")...)

	command := exec.Command(path, "--read0", "--print0", "--filter=payload-line-2", "--exact", "--select-1", "--exit-0")
	command.Env = process.SanitizeEnv(os.Environ(), nil)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("real fzf raw pipe: %v; stderr=%q", err, stderr.Bytes())
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("raw pipe bytes=%q want exact %q", stdout.Bytes(), want)
	}
	if bytes.Contains(stdout.Bytes(), []byte{'\r'}) || stdout.Len() == 0 || stdout.Bytes()[stdout.Len()-1] != 0 {
		t.Fatalf("raw pipe inserted CR or omitted final NUL: %q", stdout.Bytes())
	}
}
