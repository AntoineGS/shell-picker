package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
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
