package app

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestTokenCanaryUsesActualCallbackCredentialAndExcludesNamedSinks(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	tracePath := filepath.Join(fixture.cwd, "session.trace.jsonl")
	fixture.options.TracePath = tracePath
	var ttyOut, ttyErr bytes.Buffer
	fixture.dependencies.TTYOut = &ttyOut
	fixture.dependencies.TTYErr = &ttyErr
	var credential string
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		credential = config.CallbackToken
		if credential == "" || config.CallbackAddress == "" {
			t.Fatalf("controlled callback fields are empty")
		}
		client := callbackClient(t, config)
		if _, err := client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1}); err != nil {
			t.Fatalf("actual controlled callback credential was unusable: %v", err)
		}
		for name, data := range map[string][]byte{
			"fzf-options":             []byte(strings.Join(config.Options, "\x00")),
			"fzf-input":               config.Input,
			"noncallback-environment": []byte(strings.Join(config.Environment, "\x00")),
		} {
			if bytes.Contains(data, []byte(credential)) {
				t.Fatalf("actual credential leaked through %s", name)
			}
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	if credential == "" {
		t.Fatal("launch did not expose the actual controlled credential")
	}
	for name, data := range map[string][]byte{
		"tty-stdout":      ttyOut.Bytes(),
		"tty-stderr":      ttyErr.Bytes(),
		"trace":           mustReadSecurityFile(t, tracePath),
		"temporary-files": readTreeBytes(t, fixture.cwd),
	} {
		if bytes.Contains(data, []byte(credential)) {
			t.Fatalf("actual credential leaked through %s", name)
		}
	}
}

func mustReadSecurityFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readTreeBytes(t *testing.T, root string) []byte {
	t.Helper()
	var contents bytes.Buffer
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents.WriteString(path)
		contents.WriteByte(0)
		contents.Write(data)
		contents.WriteByte(0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return contents.Bytes()
}
