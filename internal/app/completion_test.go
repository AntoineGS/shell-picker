package app

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestCompletionCLIAndInitialPath(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	options, err := parsePickerArgs([]string{"cp", "--cwd", string(fixture.options.CWD), "--home", string(fixture.options.Home),
		"--completion", "./", "--command-line", "nvim ./", "--display", "inline"}, fixture.options.ExecutablePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		if !contains(config.Options, "--query=./") || !contains(config.Options, "--prompt=[I] ") ||
			!contains(config.Options, "--header=nvim ./") {
			t.Fatalf("missing editable input/static command: %q", config.Options)
		}
		record := recordForPath(t, config.Input, fixture.file)
		client := callbackClient(t, config)
		for _, query := range []string{"./missing/", "./"} {
			response, err := client.Event(ctx, sessionipc.EventRequest{
				Opcode: protocol.OpQuery, QueryBase64: base64.StdEncoding.EncodeToString([]byte(query)),
			})
			if err != nil || response.Effect.Header != "nvim ./" {
				t.Fatalf("command header after query %q: %+v, %v", query, response, err)
			}
			display, err := client.Display(ctx)
			if err != nil || display.Header != "nvim ./" {
				t.Fatalf("command header on display refresh: %+v, %v", display, err)
			}
		}
		return fzf.Result{Key: "enter", Records: [][]byte{record}}, nil
	}
	outcome, err := RunPicker(context.Background(), options, fixture.dependencies)
	if err != nil || len(outcome.Paths) != 1 || string(outcome.Paths[0]) != "./"+filepath.Base(fixture.file) {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestCompletionCDStillIncludesZoxide(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX zoxide fixture")
	}
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.Completion = true
	target := t.TempDir()
	fixture.dependencies.ZoxidePath = zoxideFixture(t, target)
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		record := recordForPath(t, config.Input, target)
		return fzf.Result{Key: "enter", Records: [][]byte{record}}, nil
	}
	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	want, _ := filepath.Rel(string(fixture.options.CWD), target)
	if err != nil || len(outcome.Paths) != 1 || string(outcome.Paths[0]) != want {
		t.Fatalf("outcome=%+v want=%q err=%v", outcome, want, err)
	}
}

func TestCompletionUnfinishedDirectoryDoesNotAbort(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	if err := os.WriteFile(filepath.Join(string(fixture.options.CWD), "plain-file"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.options.Completion = true
	fixture.options.Query = "plain-file/"
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		return fzf.Result{Aborted: true}, nil
	}
	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil || outcome.Status != protocol.StatusAborted {
		t.Fatalf("unfinished path aborted before the UI could recover: %+v %v", outcome, err)
	}
}
