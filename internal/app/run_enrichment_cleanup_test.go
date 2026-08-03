//go:build !windows

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerWaitsForEnrichmentBeforeClosingServer(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = cancellationZoxideFixture(t, fixture.child)
	counts := newProcessCounts()
	started := make(chan struct{}, 1)
	exitCheck := make(chan error, 1)
	var client *sessionipc.Client
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
			return
		}
		if event.Phase == "exit" {
			if client == nil {
				exitCheck <- errors.New("callback client was not initialized before zoxide exit")
				return
			}
			_, err := client.Display(context.Background())
			exitCheck <- err
		}
	}
	launchErr := errors.New("fzf failed after source start")
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		client = callbackClient(t, config)
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			return fzf.Result{}, errors.New("zoxide did not start")
		}
		return fzf.Result{}, launchErr
	}

	_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if !errors.Is(err, launchErr) {
		t.Fatalf("RunPicker err=%v, want %v", err, launchErr)
	}
	select {
	case err := <-exitCheck:
		if err != nil {
			t.Fatalf("generation load during zoxide exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide exit observer did not verify cleanup ordering")
	}
	attempts, starts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 || live != 0 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, starts, maxLive, exits, live)
	}
}
