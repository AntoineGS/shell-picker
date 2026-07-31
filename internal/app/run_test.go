package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/callback"
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerOwnsOneSessionAndOneFZF(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	launches := 0
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		launches++
		if config.CallbackAddress == "" || config.CallbackToken == "" || config.CallbackToken == "forged" ||
			!contains(config.Options, "--preview=p") || !contains(config.Options, "--prompt=[I] ") ||
			!contains(config.Options, "--header="+pathutil.PromptDisplayHome(pathutil.Filesystem(fixture.options.CWD), pathutil.Filesystem(fixture.options.Home))) ||
			!contains(config.Options, "--header-first") || !contains(config.Options, "--info-command=i:cd") {
			t.Fatalf("callback/options config=%+v", config)
		}
		for _, entry := range config.Environment {
			if strings.HasPrefix(entry, "SHELL_PICKER_") || strings.HasPrefix(entry, "FZF_") {
				t.Fatalf("uncontrolled environment=%q", config.Environment)
			}
		}
		record := recordForPath(t, config.Input, fixture.child)
		return fzf.Result{Key: "enter", Records: [][]byte{record}}, nil
	}

	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if launches != 1 || outcome.Status != protocol.StatusAccepted || len(outcome.Paths) != 1 || string(outcome.Paths[0]) != fixture.child {
		t.Fatalf("launches=%d outcome=%+v", launches, outcome)
	}
	if _, err := fixture.tty.Stat(); err != nil {
		t.Fatalf("injected terminal was closed: %v", err)
	}
}

func TestRunPickerCompactsZoxideHomeDisplay(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	home := t.TempDir()
	initialLocation := filepath.Join(home, "start")
	if err := os.Mkdir(initialLocation, 0o700); err != nil {
		t.Fatal(err)
	}
	zoxideTarget := filepath.Join(home, "visited", "project")
	fixture.options.CWD = []byte(initialLocation)
	fixture.options.Home = []byte(home)
	fixture.dependencies.ZoxidePath = zoxideFixture(t, zoxideTarget)
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		record := recordForPath(t, config.Input, zoxideTarget)
		wire, err := protocol.ParseRecord(record)
		if err != nil || wire.Display != "~/visited/project" {
			t.Fatalf("zoxide wire=%+v err=%v", wire, err)
		}
		decoded, err := protocol.DecodePath(wire.Payload)
		if err != nil || string(decoded) != zoxideTarget {
			t.Fatalf("payload=%q err=%v", decoded, err)
		}
		return fzf.Result{Key: "enter", Records: [][]byte{wire.Bytes()}}, nil
	}

	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != protocol.StatusAccepted || len(outcome.Paths) != 1 || string(outcome.Paths[0]) != zoxideTarget {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestIndependentRunPickerFreshSessionsQueryConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	started, release := make(chan struct{}, 2), make(chan struct{}, 2)
	counts := newProcessCounts()
	observer := func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
			<-release
		}
	}
	done := make(chan error, 2)
	for range 2 {
		fixture := newPickerFixture(t, protocol.PickerCD)
		fixture.options.ZoxidePolicy = candidate.ZoxideFresh
		fixture.dependencies.ZoxidePath = zoxideFixture(t, fixture.cwd)
		fixture.dependencies.ProcessRunner.Observe = observer
		fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
			return fzf.Result{Aborted: true, ExitCode: 130}, nil
		}
		go func() {
			_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			done <- err
		}()
	}
	for range 2 {
		<-started
	}
	if _, starts, maxLive := counts.values(); starts != 2 || maxLive != 2 {
		t.Fatalf("starts=%d maxLive=%d", starts, maxLive)
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunPickerShipsWorkingPreviewCallback(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	callbacks := 0
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		record := recordForPath(t, config.Input, fixture.file)
		client := callbackClient(t, config)
		var previewOutput bytes.Buffer
		err := callback.Dispatch(ctx, callback.Command{Kind: callback.KindPreview}, callback.Dependencies{
			Client: client,
			LookupEnv: func(key string) string {
				if key == "FZF_CURRENT_ITEM" {
					return string(record)
				}
				return ""
			},
			Stdout: &previewOutput, Stderr: io.Discard,
			Preview: func(_ context.Context, resolved protocol.ResolvedCandidate, stdout, _ io.Writer) error {
				contents, readErr := os.ReadFile(string(resolved.Path))
				if readErr == nil {
					_, readErr = stdout.Write(contents)
				}
				return readErr
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		callbacks++
		if previewOutput.String() != "title\n" {
			t.Fatalf("preview=%q", previewOutput.String())
		}
		return fzf.Result{Key: "enter", Records: [][]byte{record}}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 {
		t.Fatalf("preview callbacks=%d", callbacks)
	}
}

func TestRunPickerRejectsSelectionFromSupersededSnapshot(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		stale := recordForPath(t, config.Input, fixture.file)
		client := callbackClient(t, config)
		if _, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"}); err != nil {
			t.Fatal(err)
		}
		return fzf.Result{Key: "enter", Records: [][]byte{stale}}, nil
	}
	if outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err == nil {
		t.Fatalf("accepted stale outcome %+v", outcome)
	}
}

func TestRunPickerClosesCallbackEndpointBeforeReturning(t *testing.T) {
	for _, test := range []struct {
		name   string
		result func(*testing.T, fzf.Config, string) fzf.Result
		fail   bool
	}{
		{"success", func(t *testing.T, config fzf.Config, file string) fzf.Result {
			return fzf.Result{Key: "enter", Records: [][]byte{recordForPath(t, config.Input, file)}}
		}, false},
		{"abort", func(*testing.T, fzf.Config, string) fzf.Result { return fzf.Result{Aborted: true, ExitCode: 130} }, false},
		{"error", func(*testing.T, fzf.Config, string) fzf.Result { return fzf.Result{} }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCP)
			var client *sessionipc.Client
			fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
				client = callbackClient(t, config)
				if test.fail {
					return fzf.Result{}, errors.New("launch failed")
				}
				return test.result(t, config, fixture.file), nil
			}
			_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if (err != nil) != test.fail {
				t.Fatalf("err=%v fail=%v", err, test.fail)
			}
			if _, err := client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1}); err == nil {
				t.Fatal("callback endpoint remained available")
			}
		})
	}
}

func TestAbortChangesNothing(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil || outcome.Status != protocol.StatusAborted || len(outcome.Paths) != 0 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	var output bytes.Buffer
	if err := protocol.EncodeOutcome(&output, protocol.OutputNUL, outcome); err != nil || output.Len() != 0 {
		t.Fatalf("abort output=%q err=%v", output.Bytes(), err)
	}
}

func TestRunPickerAppliesZoxidePolicyProcessBudgets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	cases := []struct {
		name     string
		picker   protocol.Picker
		policy   candidate.ZoxidePolicy
		attempts int
		starts   int
	}{
		{"cached-cd", protocol.PickerCD, candidate.ZoxideCached, 1, 1},
		{"fresh-cd", protocol.PickerCD, candidate.ZoxideFresh, 1, 1},
		{"cached-cp", protocol.PickerCP, candidate.ZoxideCached, 0, 0},
		{"fresh-cp", protocol.PickerCP, candidate.ZoxideFresh, 0, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, test.picker)
			fixture.options.ZoxidePolicy = test.policy
			counts := newProcessCounts()
			fixture.dependencies.ProcessRunner.Observe = counts.observe
			fixture.dependencies.ZoxidePath = zoxideFixture(t, fixture.cwd)
			fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
				client := callbackClient(t, config)
				for range 2 {
					response, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left", QueryBase64: "", CurrentItemBase64: ""})
					if err != nil || response.Effect.ReloadGeneration == 0 {
						t.Fatalf("parent transform=%+v err=%v", response, err)
					}
					generation, err := client.Load(ctx, sessionipc.LoadRequest{Generation: response.Effect.ReloadGeneration})
					if err != nil {
						t.Fatal(err)
					}
					for _, raw := range bytes.Split(bytes.TrimSuffix(generation, []byte{0}), []byte{0}) {
						wire, err := protocol.ParseRecord(raw)
						if err != nil {
							t.Fatal(err)
						}
						if wire.Kind == protocol.KindZoxide {
							t.Fatalf("navigation generation retained zoxide record %q", raw)
						}
					}
				}
				return fzf.Result{Aborted: true, ExitCode: 130}, nil
			}
			if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
				t.Fatal(err)
			}
			attempts, starts, maxLive := counts.values()
			if attempts != test.attempts || starts != test.starts || maxLive != boolInt(test.starts > 0) {
				t.Fatalf("attempts=%d starts=%d maxLive=%d", attempts, starts, maxLive)
			}
		})
	}
}

func TestRunPickerRejectsInvalidOptionsBeforeLaunch(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	launched := false
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		launched = true
		return fzf.Result{}, nil
	}
	fixture.options.CWD = []byte("relative")
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err == nil || launched {
		t.Fatalf("err=%v launched=%v", err, launched)
	}
}

func TestSessionBuilderConfiguresIncomingDependencyInPlace(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	builder, err := sessionBuilder(fixture.options, &fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if builder != &fixture.dependencies.CandidateBuilder {
		t.Fatal("sessionBuilder returned another Builder copy")
	}
}

func TestRunPickerMissingZoxideAttemptsWithoutStarting(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	counts := newProcessCounts()
	fixture.dependencies.ProcessRunner.Observe = counts.observe
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	attempts, starts, maxLive := counts.values()
	if attempts != 1 || starts != 0 || maxLive != 0 {
		t.Fatalf("attempts=%d starts=%d maxLive=%d", attempts, starts, maxLive)
	}
}

func TestRunPickerNavigationRemovesZoxideCandidates(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	zoxideTarget := t.TempDir()
	fixture.dependencies.ZoxidePath = zoxideFixture(t, zoxideTarget)
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		zoxideRecord := recordForPath(t, config.Input, zoxideTarget)
		wire, err := protocol.ParseRecord(zoxideRecord)
		if err != nil || wire.Kind != protocol.KindZoxide {
			t.Fatalf("zoxide record=%q kind=%q err=%v", zoxideRecord, wire.Kind, err)
		}
		childRecord := recordForPath(t, config.Input, fixture.child)
		client := callbackClient(t, config)
		response, err := client.Event(ctx, sessionipc.EventRequest{
			Opcode:            protocol.OpForward,
			CurrentItemBase64: base64.StdEncoding.EncodeToString(childRecord),
		})
		if err != nil || response.Effect.ReloadGeneration == 0 {
			t.Fatalf("forward transform=%+v err=%v", response, err)
		}
		generation, err := client.Load(ctx, sessionipc.LoadRequest{Generation: response.Effect.ReloadGeneration})
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range bytes.Split(bytes.TrimSuffix(generation, []byte{0}), []byte{0}) {
			wire, err := protocol.ParseRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			if wire.Kind == protocol.KindZoxide {
				t.Fatalf("navigation generation retained zoxide record %q", raw)
			}
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
}

type pickerFixture struct {
	options      PickerOptions
	dependencies Dependencies
	cwd, child   string
	file         string
	tty          *os.File
}

func newPickerFixture(t *testing.T, picker protocol.Picker) pickerFixture {
	t.Helper()
	cwd := t.TempDir()
	child := filepath.Join(cwd, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cwd, "readme.md")
	if err := os.WriteFile(file, []byte("title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tty, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tty.Close() })
	return pickerFixture{
		cwd: cwd, child: child, file: file, tty: tty,
		options: PickerOptions{Picker: picker, CWD: []byte(cwd), Home: []byte(cwd), Output: protocol.OutputNUL,
			FZFPath: "fzf", ExecutablePath: filepath.Join(cwd, "shell-picker"),
			ZoxidePolicy: candidate.ZoxideCached, ZoxideTimeout: candidate.DefaultZoxideTimeout()},
		dependencies: Dependencies{ProcessRunner: process.Runner{}, ZoxidePath: filepath.Join(cwd, "missing-zoxide"),
			Environment: []string{"PATH=/usr/bin", "SHELL_PICKER_TOKEN=forged"}, ForegroundTTY: tty},
	}
}

func recordForPath(t *testing.T, framed []byte, wanted string) []byte {
	t.Helper()
	for _, raw := range bytes.Split(bytes.TrimSuffix(framed, []byte{0}), []byte{0}) {
		wire, err := protocol.ParseRecord(raw)
		if err != nil {
			t.Fatal(err)
		}
		path, err := protocol.DecodePath(wire.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if string(path) == wanted {
			return bytes.Clone(raw)
		}
	}
	t.Fatalf("path %q absent from %q", wanted, framed)
	return nil
}

func callbackClient(t *testing.T, config fzf.Config) *sessionipc.Client {
	t.Helper()
	values := map[string]string{"SHELL_PICKER_ADDR": config.CallbackAddress, "SHELL_PICKER_TOKEN": config.CallbackToken}
	client, err := sessionipc.NewClientFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type processCounts struct {
	mu                      sync.Mutex
	attempts, starts, exits int
	live, maxLive           int
}

func newProcessCounts() *processCounts { return &processCounts{} }
func (counts *processCounts) observe(event process.ProcessEvent) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	switch event.Phase {
	case "attempt":
		counts.attempts++
	case "start":
		counts.starts++
		counts.live++
		if counts.live > counts.maxLive {
			counts.maxLive = counts.live
		}
	case "exit":
		counts.live--
		counts.exits++
	}
}

func (counts *processCounts) lifecycleValues() (attempts, starts, maxLive, exits, live int) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	return counts.attempts, counts.starts, counts.maxLive, counts.exits, counts.live
}
func (counts *processCounts) values() (int, int, int) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	return counts.attempts, counts.starts, counts.maxLive
}

func zoxideFixture(t *testing.T, path string) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "zoxide")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' '"+path+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
