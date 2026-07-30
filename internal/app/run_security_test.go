package app

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/preview"
	"github.com/AntoineGS/shell-picker/internal/process"
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
	ownedRoot := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(ownedRoot, "tmp"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(ownedRoot, "xdg"))
	t.Setenv("LOCALAPPDATA", filepath.Join(ownedRoot, "local"))
	for _, path := range []string{os.Getenv("TMPDIR"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("LOCALAPPDATA")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	toolDirectory := filepath.Join(ownedRoot, "tools")
	buildTask20ResourceHelper(t, toolDirectory, "pdftoppm")
	fixturePath := filepath.Join(ownedRoot, "canary.pdf")
	if err := os.WriteFile(fixturePath, []byte("%PDF-1.7\ncanary fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var previewOutput bytes.Buffer
	var previewEvents []process.ProcessEvent
	var credential, callbackAddress string
	var captured process.Spec
	fixture.dependencies.ProcessRunner.Execute = func(_ context.Context, spec process.Spec) error {
		if spec.Path != fixture.options.FZFPath {
			return os.ErrNotExist
		}
		captured = spec
		address := exactEnvironmentValue(t, spec.Env, "SHELL_PICKER_ADDR")
		callbackAddress = address
		credential = exactEnvironmentValue(t, spec.Env, "SHELL_PICKER_TOKEN")
		client, err := sessionipc.NewClientFromEnv(func(key string) string {
			switch key {
			case "SHELL_PICKER_ADDR":
				return address
			case "SHELL_PICKER_TOKEN":
				return credential
			default:
				return ""
			}
		})
		if err != nil {
			t.Fatalf("controlled callback fields are invalid: %v", err)
		}
		if _, err := client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1}); err != nil {
			t.Fatalf("actual controlled callback credential was unusable: %v", err)
		}
		client.CloseIdleConnections()
		cache, err := preview.NewCache("", 8<<20)
		if err != nil {
			t.Fatalf("create production preview cache: %v", err)
		}
		info, err := os.Stat(fixturePath)
		if err != nil {
			t.Fatal(err)
		}
		baseEnvironment := []string{"PATH=" + toolDirectory, "TERM=xterm-256color"}
		baseEnvironment = append(baseEnvironment, fixture.dependencies.Environment...)
		baseEnvironment = append(baseEnvironment, "SHELL_PICKER_ADDR="+address, "SHELL_PICKER_TOKEN="+credential)
		err = preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindFile,
			Path: []byte(fixturePath), Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())},
			preview.Options{Environment: previewEnvironment(baseEnvironment), Runner: process.Runner{Observe: func(event process.ProcessEvent) {
				previewEvents = append(previewEvents, event)
			}}, Cache: cache,
				Stdout: &previewOutput, Stderr: &previewOutput})
		if err != nil {
			t.Fatalf("production preview helper: %v", err)
		}
		return &process.ExitError{Code: 130}
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	if credential == "" {
		t.Fatal("launch did not expose the actual controlled credential")
	}
	input, err := io.ReadAll(captured.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(toolDirectory, "environment.log")); err != nil {
		t.Fatalf("renderer environment sink missing: events=%v", previewEvents)
	}
	for name, data := range map[string][]byte{
		"arguments":            []byte(strings.Join(captured.Args, "\x00")),
		"fzf-input":            input,
		"base-environment":     []byte(strings.Join(fixture.dependencies.Environment, "\x00")),
		"tty-stdout":           ttyOut.Bytes(),
		"tty-stderr":           ttyErr.Bytes(),
		"trace":                mustReadSecurityFile(t, tracePath),
		"temporary-files":      readTreeBytes(t, fixture.cwd),
		"renderer-environment": mustReadSecurityFile(t, filepath.Join(toolDirectory, "environment.log")),
		"preview-output":       previewOutput.Bytes(),
		"cache-and-temp":       readTreeBytes(t, ownedRoot),
	} {
		if containsControlledValue(data, credential, callbackAddress) {
			t.Fatalf("controlled callback value leaked through %s", name)
		}
	}
	cacheRoot := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "shell-picker", "previews")
	if runtime.GOOS == "windows" {
		cacheRoot = filepath.Join(os.Getenv("LOCALAPPDATA"), "shell-picker", "previews")
	}
	assertTask20CacheHasWinnerAndNoStage(t, cacheRoot)
}

func containsControlledValue(data []byte, values ...string) bool {
	for _, value := range values {
		if value != "" && bytes.Contains(data, []byte(value)) {
			return true
		}
	}
	return false
}

func exactEnvironmentValue(t *testing.T, environment []string, key string) string {
	t.Helper()
	prefix, count, value := key+"=", 0, ""
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			count++
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	if count != 1 || value == "" {
		t.Fatalf("controlled %s count=%d empty=%v", key, count, value == "")
	}
	return value
}

func buildTask20ResourceHelper(t *testing.T, directory, name string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(directory, name)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", path, "./integration/testhelper/resource")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build resource helper: %v: %s", err, output)
	}
	return path
}

func assertTask20CacheHasWinnerAndNoStage(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	winners := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".shell-picker-preview-") {
			t.Fatalf("preview stage remained in cache root")
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if len(entry.Name()) == 64 && info.Mode().IsRegular() {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("cache winner count=%d", winners)
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
