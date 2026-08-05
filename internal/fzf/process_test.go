package fzf

import (
	"bytes"
	"context"
	"encoding/base64"
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
	for index, argument := range spec.Args {
		if strings.Contains(argument, config.CallbackAddress) || strings.Contains(argument, config.CallbackToken) {
			t.Fatalf("controlled callback credential reached argument %d", index)
		}
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
	t.Run("passes only controlled trace identity", testSessionSpecPassesOnlyControlledTraceIdentityToCallbacks)
	t.Run("does not inherit forged trace identity", testSessionSpecDoesNotInheritForgedTraceIdentity)
}

func testSessionSpecPassesOnlyControlledTraceIdentityToCallbacks(t *testing.T) {
	config := testConfig()
	config.TracePath = filepath.Join(t.TempDir(), "trace.jsonl")
	config.TraceSession = "sha256:0123456789abcdef"
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvExactlyOnce(t, spec.Env, "SHELL_PICKER_TRACE_PATH", config.TracePath)
	assertEnvExactlyOnce(t, spec.Env, "SHELL_PICKER_TRACE_SESSION", config.TraceSession)
	for _, entry := range spec.Env {
		if strings.Contains(entry, "FZF_API_KEY") {
			t.Fatalf("sidecar credential reached trace environment=%q", entry)
		}
	}
}

func testSessionSpecDoesNotInheritForgedTraceIdentity(t *testing.T) {
	config := testConfig()
	config.Environment = []string{"PATH=/bin", "SHELL_PICKER_TRACE_PATH=forged", "SHELL_PICKER_TRACE_SESSION=forged"}
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "SHELL_PICKER_TRACE_PATH=") || strings.HasPrefix(entry, "SHELL_PICKER_TRACE_SESSION=") {
			t.Fatalf("forged trace identity inherited: %q", entry)
		}
	}
}

func TestSessionSpecUsesForegroundTTYForUIStderrByDefault(t *testing.T) {
	config := testConfig()
	config.TTYErr = nil
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if spec.Stderr != nil {
			t.Fatalf("stderr=%T want nil without a foreground terminal", spec.Stderr)
		}
		return
	}
	if spec.Stderr != config.ForegroundTTY {
		t.Fatalf("stderr=%T want foreground tty", spec.Stderr)
	}
}

func TestSessionSpecForwardsInputReaderDirectly(t *testing.T) {
	config := testConfig()
	input := NewInputStream([]byte("record\x00"))
	config.Input = input
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Stdin != input {
		t.Fatalf("spec.Stdin=%T %p, want input %p", spec.Stdin, spec.Stdin, input)
	}
	if !spec.CloseStdinOnExit {
		t.Fatal("fzf session did not opt in to closing pumped stdin on child exit")
	}
}

func TestPrepareSessionRejectsNilInput(t *testing.T) {
	config := testConfig()
	config.Input = nil
	if _, _, err := prepareSession(config); err == nil || err.Error() != "fzf: nil input" {
		t.Fatalf("prepareSession() error = %v, want fzf: nil input", err)
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

func TestSessionSpecDisabledPreservesInheritedFZFAPIKey(t *testing.T) {
	config := testConfig()
	config.Environment = append(config.Environment, "FZF_API_KEY=inherited-key")
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvironmentValue(t, spec.Env, "FZF_API_KEY", "inherited-key")
}

func TestSessionSpecEnabledOverridesInheritedFZFAPIKey(t *testing.T) {
	config := testConfig()
	config.Options = mustListenOptions()
	config.ListenAPIKey = testListenAPIKey()
	config.Environment = append(config.Environment, "FZF_API_KEY=stale", "fzf_api_key=case-stale",
		"SHELL_PICKER_EXPERIMENTAL_FZF_SIDECAR=1")
	wantOptions := slices.Clone(config.Options)
	wantEnvironment := slices.Clone(config.Environment)

	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvironmentValue(t, spec.Env, "FZF_API_KEY", config.ListenAPIKey)
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "SHELL_PICKER_EXPERIMENTAL_FZF_SIDECAR=") {
			t.Fatalf("sidecar activation reached fzf environment: %q", entry)
		}
	}
	if !slices.Equal(config.Options, wantOptions) || !slices.Equal(config.Environment, wantEnvironment) {
		t.Fatalf("prepareSession mutated config: options=%q environment=%q", config.Options, config.Environment)
	}
}

func TestPrepareSessionValidatesListenAPIKeyContract(t *testing.T) {
	validKey := testListenAPIKey()
	invalidLengthKey := base64.RawURLEncoding.EncodeToString(make([]byte, 31))
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "enabled without key",
			mutate: func(config *Config) {
				config.Options = mustListenOptions()
			},
		},
		{
			name: "key without listen option",
			mutate: func(config *Config) {
				config.ListenAPIKey = validKey
			},
		},
		{
			name: "duplicate listen options",
			mutate: func(config *Config) {
				config.Options = append(mustListenOptions(), "--listen=127.0.0.1:4321")
				config.ListenAPIKey = validKey
			},
		},
		{
			name: "malformed listen option",
			mutate: func(config *Config) {
				config.Options = append(config.Options, "--listen=127.0.0.1:4321,change-header:forged")
				config.ListenAPIKey = validKey
			},
		},
		{
			name: "wrong key length",
			mutate: func(config *Config) {
				config.Options = mustListenOptions()
				config.ListenAPIKey = invalidLengthKey
			},
		},
		{
			name: "wrong key alphabet",
			mutate: func(config *Config) {
				config.Options = mustListenOptions()
				config.ListenAPIKey = strings.Repeat("!", 43)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			secret := validKey
			test.mutate(&config)
			if _, _, err := prepareSession(config); err == nil {
				t.Fatal("prepareSession succeeded")
			} else if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked API key: %v", err)
			}
		})
	}
}

func TestSessionSpecKeyCanaryDoesNotReachArgumentsErrorsOrObserver(t *testing.T) {
	config := testConfig()
	config.Options = mustListenOptions()
	config.ListenAPIKey = testListenAPIKey()
	spec, _, err := prepareSession(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, argument := range spec.Args {
		if strings.Contains(argument, config.ListenAPIKey) {
			t.Fatalf("argument leaked API key: %q", argument)
		}
	}
	var observed []processpkg.ProcessEvent
	config.Runner.Observe = func(event processpkg.ProcessEvent) {
		observed = append(observed, event)
	}
	_, err = Run(context.Background(), config)
	if err == nil {
		t.Fatal("Run succeeded with missing fzf executable")
	}
	if strings.Contains(err.Error(), config.ListenAPIKey) {
		t.Fatalf("Run error leaked API key: %v", err)
	}
	for _, event := range observed {
		if strings.Contains(event.Path, config.ListenAPIKey) {
			t.Fatalf("process observer leaked API key: %+v", event)
		}
	}
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
	result, err := Run(context.Background(), config)
	if attempts != 1 {
		t.Fatalf("attempts=%d err=%v result=%+v", attempts, err, result)
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
	result, err := Run(context.Background(), config)
	if len(paths) != 1 {
		t.Fatalf("process attempts=%q err=%v result=%+v", paths, err, result)
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
	input := NewInputStream([]byte("record\x00"))
	_ = input.Close()
	options, err := Options(OptionsConfig{Picker: protocol.PickerCP, Prompt: "[N] ", Header: "/work/"})
	if err != nil {
		panic(err)
	}
	return Config{
		Picker:          protocol.PickerCP,
		FZFPath:         filepath.Join(os.TempDir(), "missing-fzf"),
		ExecutablePath:  filepath.Join(os.TempDir(), "shell-picker"),
		Environment:     []string{"PATH=/bin", "FZF_DEFAULT_OPTS=forged", "SHELL_PICKER_ADDR=http://forged", "SHELL_PICKER_TOKEN=forged"},
		CallbackAddress: "http://127.0.0.1:4321",
		CallbackToken:   "controlled-token",
		Options:         options,
		Input:           input,
		Runner:          processpkg.Runner{},
		ForegroundTTY:   nonTerminalFile(),
	}
}

func mustListenOptions() []string {
	options, err := Options(OptionsConfig{
		Picker:        protocol.PickerCP,
		Prompt:        "[N] ",
		Header:        "/work/",
		ListenAddress: "127.0.0.1:4321",
	})
	if err != nil {
		panic(err)
	}
	return options
}

func testListenAPIKey() string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, 32))
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

func assertEnvironmentValue(t *testing.T, environment []string, key, wantValue string) {
	t.Helper()
	count := 0
	for _, entry := range environment {
		candidate, value, ok := strings.Cut(entry, "=")
		if !ok || candidate != key && (runtime.GOOS != "windows" || !strings.EqualFold(candidate, key)) {
			continue
		}
		count++
		if value != wantValue {
			t.Fatalf("%s entry=%q want value %q", key, entry, wantValue)
		}
	}
	if count != 1 {
		t.Fatalf("%s count=%d environment=%q", key, count, environment)
	}
}
