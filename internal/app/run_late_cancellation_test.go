package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerLateParentCancellationOverridesAcceptedAndAbortedResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		result func(*testing.T, fzf.Config, pickerFixture) fzf.Result
	}{
		{"accepted", func(t *testing.T, config fzf.Config, fixture pickerFixture) fzf.Result {
			return fzf.Result{Key: "enter", Records: [][]byte{recordForPath(t, config.Input, fixture.file)}}
		}},
		{"aborted", func(*testing.T, fzf.Config, pickerFixture) fzf.Result {
			return fzf.Result{Aborted: true, ExitCode: 130}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCP)
			cause := errors.New("parent cancelled after fzf")
			ctx, cancel := context.WithCancelCause(context.Background())
			var client *sessionipc.Client
			fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
				client = callbackClient(t, config)
				result := test.result(t, config, fixture)
				cancel(cause)
				return result, nil
			}
			outcome, err := RunPicker(ctx, fixture.options, fixture.dependencies)
			if err != cause || outcome.Status != "" || len(outcome.Paths) != 0 {
				t.Fatalf("outcome=%+v err=%v want exact cause=%v", outcome, err, cause)
			}
			if _, err := client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1}); err == nil {
				t.Fatal("callback endpoint remained available after late cancellation")
			}
		})
	}
}

func TestPickerCLILateCancellationEmitsNoAcceptedOrAbortedOutcome(t *testing.T) {
	for _, test := range []struct {
		name   string
		result func(*testing.T, fzf.Config, pickerFixture) fzf.Result
	}{
		{"accepted", func(t *testing.T, config fzf.Config, fixture pickerFixture) fzf.Result {
			return fzf.Result{Key: "enter", Records: [][]byte{recordForPath(t, config.Input, fixture.file)}}
		}},
		{"aborted", func(*testing.T, fzf.Config, pickerFixture) fzf.Result {
			return fzf.Result{Aborted: true, ExitCode: 130}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCP)
			ctx, cancel := context.WithCancelCause(context.Background())
			fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
				result := test.result(t, config, fixture)
				cancel(errors.New("late CLI cancellation"))
				return result, nil
			}
			args := []string{"cp", "--cwd", fixture.cwd, "--home", fixture.cwd, "--output", "nuon"}
			var stdout, stderr bytes.Buffer
			code := runPickerCLI(ctx, args, Streams{Out: &stdout, Err: &stderr}, filepath.Join(fixture.cwd, "shell-picker"), &fixture.dependencies)
			if code != 1 || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestSelectLifecycleErrorPrefersActorCloseOverParentCancellation(t *testing.T) {
	selected := errors.New("launch or server failed")
	actorClose := errors.New("actor close failed")
	parentCause := errors.New("parent cancelled")
	for _, test := range []struct {
		name                          string
		selected, actor, parent, want error
	}{
		{"selected lifecycle error", selected, actorClose, parentCause, selected},
		{"actor close over cancellation", nil, actorClose, parentCause, actorClose},
		{"clean actor uses exact cancellation", nil, nil, parentCause, parentCause},
		{"clean lifecycle", nil, nil, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := selectLifecycleError(test.selected, test.actor, test.parent); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}

func TestRunPickerFZFChildExitClosesInputWithoutOverridingResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX child fixture")
	}
	for _, test := range []struct {
		name   string
		result func([]byte) fzf.Result
		status protocol.Status
	}{
		{name: "accepted", result: func(record []byte) fzf.Result {
			return fzf.Result{Key: "enter", Records: [][]byte{record}}
		}, status: protocol.StatusAccepted},
		{name: "aborted", result: func([]byte) fzf.Result {
			return fzf.Result{Aborted: true, ExitCode: 130}
		}, status: protocol.StatusAborted},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCD)
			fixture.options.ZoxidePolicy = candidate.ZoxideFresh
			fixture.options.TracePath = filepath.Join(t.TempDir(), "stream-close.trace.jsonl")
			fixture.options.FZFPath = exitProcessFixture(t)
			localStarted := make(chan struct{})
			zoxideStarted := make(chan struct{})
			zoxideRelease := make(chan struct{})
			zoxideDone := make(chan struct{})
			var localOnce, zoxideStartOnce, zoxideDoneOnce, releaseOnce sync.Once
			fixture.dependencies.buildLocal = func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
				localOnce.Do(func() { close(localStarted) })
				return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, fixture.child)}, Metrics: candidate.SourceMetrics{ZoxideOutcome: "not-run"}}, nil
			}
			fixture.dependencies.loadInitialZoxide = func(ctx context.Context) (candidate.InitialZoxideResult, error) {
				zoxideStartOnce.Do(func() { close(zoxideStarted) })
				select {
				case <-zoxideRelease:
				case <-ctx.Done():
					return candidate.InitialZoxideResult{}, context.Cause(ctx)
				}
				zoxideDoneOnce.Do(func() { close(zoxideDone) })
				return enrichmentSource(t.TempDir()), nil
			}
			fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
				select {
				case <-localStarted:
				case <-time.After(2 * time.Second):
					return fzf.Result{}, errors.New("local build did not start")
				}
				select {
				case <-zoxideStarted:
				case <-time.After(2 * time.Second):
					return fzf.Result{}, errors.New("zoxide source did not start")
				}
				record := recordForPath(t, config.Input, fixture.child)
				runner := config.Runner
				observe := runner.Observe
				runner.Observe = func(event process.ProcessEvent) {
					if observe != nil {
						observe(event)
					}
					if event.Phase == "exit" && event.Path == config.FZFPath {
						releaseOnce.Do(func() { close(zoxideRelease) })
					}
				}
				if err := runner.Run(ctx, process.Spec{
					Path: config.FZFPath, Stdin: config.Input, Stdout: io.Discard, Stderr: io.Discard,
					CloseStdinOnExit: true, Containment: process.ContainmentOwnTree,
				}); err != nil {
					return fzf.Result{}, err
				}
				select {
				case <-zoxideDone:
				case <-time.After(2 * time.Second):
					return fzf.Result{}, errors.New("zoxide source did not finish after child exit")
				}
				return test.result(record), nil
			}

			outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if err != nil || outcome.Status != test.status {
				t.Fatalf("outcome=%+v err=%v want status=%q", outcome, err, test.status)
			}
			trace, err := os.ReadFile(fixture.options.TracePath)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(trace, []byte(`"generation":2`)) {
				t.Fatalf("closed fzf input still published enrichment: %s", trace)
			}
		})
	}
}

func exitProcessFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fzf-exit")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
