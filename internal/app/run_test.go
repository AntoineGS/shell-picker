package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestRunPickerCPClosesLocalInputBeforeFZFLaunch(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	counts := newProcessCounts()
	fixture.dependencies.ProcessRunner.Observe = counts.observe
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		framed, err := io.ReadAll(config.Input)
		if err != nil {
			return fzf.Result{}, err
		}
		if _, err := recordForFramedPathE(framed, fixture.file); err != nil {
			return fzf.Result{}, err
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil || outcome.Status != protocol.StatusAborted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	attempts, starts, maxLive := counts.values()
	if attempts != 0 || starts != 0 || maxLive != 0 {
		t.Fatalf("zoxide processes=(attempts=%d starts=%d maxLive=%d)", attempts, starts, maxLive)
	}
}

func TestRunPickerCompactsZoxideHomeDisplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
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

func TestRunPickerPublishesCompletedZoxideBeforeFZFConsumesInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	fixture := newPickerFixture(t, protocol.PickerCD)
	zoxideTarget := t.TempDir()
	fixture.dependencies.ZoxidePath = zoxideFixture(t, zoxideTarget)
	counts := newProcessCounts()
	zoxideExited := make(chan struct{}, 1)
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "exit" {
			zoxideExited <- struct{}{}
		}
	}
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		select {
		case <-zoxideExited:
		case <-time.After(2 * time.Second):
			return fzf.Result{}, errors.New("zoxide did not complete before fzf input consumption")
		}
		records := readFramedRecordsUntil(t, config.Input, fixture.child, zoxideTarget)
		for _, path := range []string{fixture.child, zoxideTarget} {
			wire, err := protocol.ParseRecord(records[path])
			if err != nil {
				return fzf.Result{}, err
			}
			if path == zoxideTarget && wire.Kind != protocol.KindZoxide {
				return fzf.Result{}, fmt.Errorf("zoxide record kind=%q", wire.Kind)
			}
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil || outcome.Status != protocol.StatusAborted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	attempts, starts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 || live != 0 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, starts, maxLive, exits, live)
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
		response, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
		if err != nil {
			t.Fatal(err)
		}
		finalizeTestEvent(t, ctx, client, response, false)
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
					finalizeTestEvent(t, ctx, client, response, true)
					generation, err := client.Load(ctx, sessionipc.LoadRequest{Generation: response.Effect.ReloadGeneration, EventID: response.EventID})
					if err != nil {
						t.Fatal(err)
					}
					finalizeTestLoad(t, ctx, client, response.EventID, true)
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
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	fixture := newPickerFixture(t, protocol.PickerCD)
	zoxideTarget := t.TempDir()
	fixture.dependencies.ZoxidePath = zoxideFixture(t, zoxideTarget)
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		records := readFramedRecordsUntil(t, config.Input, zoxideTarget, fixture.child)
		zoxideRecord := records[zoxideTarget]
		wire, err := protocol.ParseRecord(zoxideRecord)
		if err != nil || wire.Kind != protocol.KindZoxide {
			t.Fatalf("zoxide record=%q kind=%q err=%v", zoxideRecord, wire.Kind, err)
		}
		childRecord := records[fixture.child]
		client := callbackClient(t, config)
		response, err := client.Event(ctx, sessionipc.EventRequest{
			Opcode:            protocol.OpForward,
			CurrentItemBase64: base64.StdEncoding.EncodeToString(childRecord),
		})
		if err != nil || response.Effect.ReloadGeneration == 0 {
			t.Fatalf("forward transform=%+v err=%v", response, err)
		}
		finalizeTestEvent(t, ctx, client, response, true)
		generation, err := client.Load(ctx, sessionipc.LoadRequest{Generation: response.Effect.ReloadGeneration, EventID: response.EventID})
		if err != nil {
			t.Fatal(err)
		}
		finalizeTestLoad(t, ctx, client, response.EventID, true)
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
