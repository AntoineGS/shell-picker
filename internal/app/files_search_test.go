package app

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunCDForcedFilesSearchReturnsInsertionExitCode(t *testing.T) {
	t.Run("browser", func(t *testing.T) { testCDForcedFilesSearch(t, false) })
	t.Run("completion", func(t *testing.T) { testCDForcedFilesSearch(t, true) })
}

func testCDForcedFilesSearch(t *testing.T, completion bool) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	path := filepath.Join(fixture.cwd, "report.txt")
	second := filepath.Join(fixture.cwd, "second.txt")
	fixture.dependencies.IndexLoader = func(context.Context, string) (*fsearchdb.Database, error) {
		return &fsearchdb.Database{Entries: []fsearchdb.Entry{{Path: []byte(path)}, {Path: []byte(second)}}}, nil
	}
	fixture.dependencies.loadInitialZoxide = func(context.Context) (candidate.InitialZoxideResult, error) {
		return candidate.InitialZoxideResult{}, nil
	}
	var backend sessionipc.Backend
	fixture.dependencies.listenIPC = func(ctx context.Context, token sessionipc.Token, value sessionipc.Backend) (*sessionipc.Server, error) {
		backend = value
		return sessionipc.Listen(ctx, token, value)
	}
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		if _, err := io.ReadAll(config.Input); err != nil {
			t.Fatal(err)
		}
		result, err := backend.HandleEvent(ctx, protocol.Event{Opcode: protocol.Opcode("fa")})
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.(sessionipc.EventFinalizer).FinalizeEvent(ctx, sessionipc.EventFinalizeRequest{
			EventID: result.EventID, Applied: true,
		}); err != nil {
			t.Fatal(err)
		}
		data, err := backend.LoadGeneration(ctx, sessionipc.LoadRequest{Generation: result.ReloadGeneration, EventID: result.EventID})
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.(sessionipc.LoadFinalizer).FinalizeLoad(ctx, sessionipc.LoadFinalizeRequest{
			EventID: result.EventID, Applied: true,
		}); err != nil {
			t.Fatal(err)
		}
		records := bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0})
		return fzf.Result{Key: "enter", Records: records}, nil
	}
	var stdout, stderr bytes.Buffer
	args := []string{"cd", "--cwd", fixture.cwd, "--home", fixture.cwd}
	want := path + "\x00" + second + "\x00"
	if completion {
		args = append(args, "--completion", "")
		want = "report.txt\x00second.txt\x00"
	}
	code := runPickerCLI(context.Background(), args,
		Streams{Out: &stdout, Err: &stderr}, fixture.options.ExecutablePath, &fixture.dependencies)
	if code != 10 || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("code=%d output=%q error=%q", code, stdout.String(), stderr.String())
	}
}
