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
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerParentCancellationStopsActiveCallbackGenerationBeforeFZFReturns(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = cancellationZoxideFixture(t, fixture.cwd)
	starts := make(chan struct{}, 2)
	counts := newProcessCounts()
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			starts <- struct{}{}
		}
	}
	generationStarted := make(chan struct{})
	type callbackResult struct {
		response sessionipc.EventResponse
		err      error
	}
	callbackDone := make(chan callbackResult, 1)
	var client *sessionipc.Client
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		<-starts // Initial generation completed before fzf launch.
		values := map[string]string{"SHELL_PICKER_ADDR": config.CallbackAddress, "SHELL_PICKER_TOKEN": config.CallbackToken}
		var err error
		client, err = sessionipc.NewClientFromEnv(func(key string) string { return values[key] })
		if err != nil {
			return fzf.Result{}, err
		}
		go func() {
			response, eventErr := client.Event(context.Background(), sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
			callbackDone <- callbackResult{response: response, err: eventErr}
		}()
		select {
		case <-starts:
			close(generationStarted)
		case <-time.After(2 * time.Second):
			return fzf.Result{}, errors.New("callback generation did not start")
		}
		<-ctx.Done()
		select {
		case callback := <-callbackDone:
			if callback.err == nil || callback.response.Effect.ReloadGeneration != 0 {
				return fzf.Result{}, fmt.Errorf("callback published after cancellation: %+v err=%v", callback.response, callback.err)
			}
		case <-time.After(time.Second):
			return fzf.Result{}, errors.New("callback remained active after parent cancellation")
		}
		return fzf.Result{}, context.Cause(ctx)
	}

	cause := errors.New("parent stopped picker")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(ctx, fixture.options, fixture.dependencies)
		done <- err
	}()
	<-generationStarted
	cancel(cause)
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("RunPicker err=%v want cause=%v", err, cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPicker did not join cancelled lifecycle")
	}
	if _, err := client.Event(context.Background(), sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"}); err == nil {
		t.Fatal("callback endpoint accepted work after cancellation")
	}
	attempts, processStarts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 2 || processStarts != 2 || maxLive != 1 || exits != 2 || live != 0 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, processStarts, maxLive, exits, live)
	}
}

func cancellationZoxideFixture(t *testing.T, path string) string {
	t.Helper()
	directory := t.TempDir()
	counter := filepath.Join(directory, "count")
	executable := filepath.Join(directory, "zoxide")
	script := fmt.Sprintf("#!/bin/sh\nif [ ! -e '%s' ]; then : > '%s'; printf '%%s\\n' '%s'; exit 0; fi\nsleep 10\nprintf '%%s\\n' '%s'\n",
		counter, counter, path, path)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable
}
