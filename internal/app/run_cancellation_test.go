//go:build !windows

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRunPickerParentCancellationStopsInitialZoxideBeforeFZFLaunch(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = cancellationZoxideFixture(t, fixture.cwd)
	started := make(chan struct{}, 1)
	counts := newProcessCounts()
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
		}
	}
	fzfLaunched := make(chan struct{}, 1)
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		fzfLaunched <- struct{}{}
		return fzf.Result{}, errors.New("fzf launched before initial zoxide completed")
	}

	cause := errors.New("parent stopped picker")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(ctx, fixture.options, fixture.dependencies)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial zoxide did not start")
	}
	cancel(cause)
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("RunPicker err=%v want cause=%v", err, cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPicker did not join cancelled lifecycle")
	}
	select {
	case <-fzfLaunched:
		t.Fatal("launchFZF was called")
	default:
	}
	attempts, processStarts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || processStarts != 1 || maxLive != 1 || exits != 1 || live != 0 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, processStarts, maxLive, exits, live)
	}
}

func cancellationZoxideFixture(t *testing.T, path string) string {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "zoxide")
	script := fmt.Sprintf("#!/bin/sh\nsleep 10\nprintf '%%s\\n' '%s'\n", path)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable
}
