package fzf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSessionSpecSeparatesCallbackCredentialsFromInheritedEnvironment(t *testing.T) {
	config := testConfig()
	config.Environment = []string{
		"PATH=/first", "PATH=/bin", "FZF_DEFAULT_OPTS=bad", "FZF_DEFAULT_OPTS_FILE=/tmp/bad",
		"FZF_KEY=x", "FZF_QUERY=y", "FZF_CURRENT_ITEM=z", "SHELL_PICKER_ADDR=http://forged",
		"SHELL_PICKER_TOKEN=forged", "KEEP=yes",
	}
	config.ExecutablePath = filepath.Join(t.TempDir(), "directory with spaces", "shell-picker")
	var terminal bytes.Buffer
	config.TTYErr = &terminal
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Path != config.FZFPath || spec.Containment != processpkg.ContainmentForegroundTree ||
		spec.ForegroundTTY != config.ForegroundTTY || spec.WaitDelay != time.Second || spec.Stderr != config.TTYErr {
		t.Fatalf("spec=%+v", spec)
	}
	if spec.Stdout == config.TTYOut {
		t.Fatal("result stdout was attached to terminal output")
	}
	if got := spec.Args[len(spec.Args)-1]; got != "--with-shell=shell-picker --fzf-shell" {
		t.Fatalf("with-shell=%q", got)
	}
	if !slices.Equal(spec.Args[:len(spec.Args)-1], config.Options) {
		t.Fatalf("args=%q options=%q", spec.Args, config.Options)
	}
	wantPath := filepath.Dir(config.ExecutablePath) + string(os.PathListSeparator) + "/bin"
	assertEnvExactlyOnce(t, spec.Env, "PATH", wantPath)
	assertEnvExactlyOnce(t, spec.Env, "SHELL_PICKER_ADDR", config.CallbackAddress)
	assertEnvExactlyOnce(t, spec.Env, "SHELL_PICKER_TOKEN", config.CallbackToken)
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "FZF_") || strings.Contains(entry, "forged") || strings.Contains(entry, "/first") {
			t.Fatalf("unsanitized environment entry %q in %q", entry, spec.Env)
		}
	}
	if !slices.Contains(spec.Env, "KEEP=yes") {
		t.Fatalf("environment=%q", spec.Env)
	}
}

func TestSessionSpecUsesForegroundTTYForUIStderrByDefault(t *testing.T) {
	config := testConfig()
	config.TTYErr = nil
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Stderr != config.ForegroundTTY {
		t.Fatalf("stderr=%T want foreground tty", spec.Stderr)
	}
}

func TestSessionSpecPrependsDirectoryToEmptyInheritedPath(t *testing.T) {
	config := testConfig()
	config.Environment = []string{"KEEP=yes"}
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Dir(config.ExecutablePath) + string(os.PathListSeparator)
	assertEnvExactlyOnce(t, spec.Env, "PATH", want)
}

func TestCheckVersionAcceptsMinimumAndRejectsOlder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, test := range []struct {
		name    string
		version string
		wantErr bool
	}{
		{"minimum", "0.74.1 (fixture)\n", false},
		{"newer", "1.0.0\n", false},
		{"older patch", "0.74.0\n", true},
		{"older minor", "0.73.9\n", true},
		{"malformed", "fzf latest\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fzf")
			script := "#!/bin/sh\nprintf '%s' '" + test.version + "'\n"
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := CheckVersion(context.Background(), processpkg.Runner{}, path)
			if (err != nil) != test.wantErr {
				t.Fatalf("CheckVersion() err=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestRunRejectsUnsafeConfigurationBeforeProcessStart(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty callback address", func(c *Config) { c.CallbackAddress = "" }},
		{"empty callback token", func(c *Config) { c.CallbackToken = "" }},
		{"empty executable", func(c *Config) { c.ExecutablePath = "" }},
		{"unsafe basename", func(c *Config) { c.ExecutablePath = filepath.Join(t.TempDir(), "picker;id") }},
		{"path-list separator in directory", func(c *Config) { c.ExecutablePath = filepath.Join("bad"+string(os.PathListSeparator)+"dir", "picker") }},
		{"missing Unix terminal", func(c *Config) { c.ForegroundTTY = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.name == "missing Unix terminal" {
				t.Skip("Unix-only terminal requirement")
			}
			attempts := 0
			config := testConfig()
			config.Runner.Observe = func(event processpkg.ProcessEvent) {
				if event.Phase == "attempt" {
					attempts++
				}
			}
			test.mutate(&config)
			if _, err := Run(context.Background(), config); err == nil {
				t.Fatal("Run succeeded")
			}
			if attempts != 0 {
				t.Fatalf("started %d processes", attempts)
			}
		})
	}
}

func TestRunAllowsExecutableDirectoryWithSpacesPastValidation(t *testing.T) {
	attempts := 0
	config := testConfig()
	config.ExecutablePath = filepath.Join(t.TempDir(), "directory with spaces", "picker")
	config.Runner.Observe = func(event processpkg.ProcessEvent) {
		if event.Phase == "attempt" {
			attempts++
		}
	}
	_, _ = Run(context.Background(), config)
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestRunDoesNotProbeVersion(t *testing.T) {
	var paths []string
	config := testConfig()
	config.Runner.Observe = func(event processpkg.ProcessEvent) {
		if event.Phase == "attempt" {
			paths = append(paths, event.Path)
		}
	}
	_, _ = Run(context.Background(), config)
	if len(paths) != 1 {
		t.Fatalf("process attempts=%q", paths)
	}
	for _, path := range paths {
		if strings.Contains(path, "--version") {
			t.Fatalf("version probe attempt=%q", path)
		}
	}
}

func TestInstalledFZFCheckVersion(t *testing.T) {
	path := os.Getenv("SHELL_PICKER_REAL_FZF")
	if path == "" {
		t.Skip("SHELL_PICKER_REAL_FZF is required for the installed-version gate")
	}
	if err := CheckVersion(context.Background(), processpkg.Runner{}, path); err != nil {
		t.Fatal(err)
	}
}

func testConfig() Config {
	return Config{
		Picker:          protocol.PickerCP,
		FZFPath:         filepath.Join(os.TempDir(), "missing-fzf"),
		ExecutablePath:  filepath.Join(os.TempDir(), "shell-picker"),
		Environment:     []string{"PATH=/bin", "FZF_DEFAULT_OPTS=forged", "SHELL_PICKER_ADDR=http://forged", "SHELL_PICKER_TOKEN=forged"},
		CallbackAddress: "http://127.0.0.1:4321",
		CallbackToken:   "controlled-token",
		Options:         Options(protocol.PickerCP, "[N] /work/ "),
		Input:           []byte("record\x00"),
		Runner:          processpkg.Runner{},
		ForegroundTTY:   nonTerminalFile(),
	}
}

func nonTerminalFile() *os.File {
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Stdin
}

func assertEnvExactlyOnce(t *testing.T, environment []string, key, wantValue string) {
	t.Helper()
	prefix, count := key+"=", 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			count++
			if entry != prefix+wantValue {
				t.Fatalf("%s entry=%q want=%q", key, entry, prefix+wantValue)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s count=%d environment=%q", key, count, environment)
	}
}
