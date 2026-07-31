package callback

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	previewpkg "github.com/AntoineGS/shell-picker/internal/preview"
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type fakeClient struct {
	events    []sessionipc.EventRequest
	loads     []sessionipc.LoadRequest
	previews  []sessionipc.PreviewRequest
	displays  int
	effect    protocol.Effect
	load      []byte
	display   sessionipc.DisplayResponse
	resolved  sessionipc.PreviewResponse
	err       error
	recordErr error
}

type panicClient struct{}

func (panicClient) Event(context.Context, sessionipc.EventRequest) (sessionipc.EventResponse, error) {
	panic("unexpected IPC Event")
}
func (panicClient) Load(context.Context, sessionipc.LoadRequest) ([]byte, error) {
	panic("unexpected IPC Load")
}
func (panicClient) ResolvePreview(context.Context, sessionipc.PreviewRequest) (sessionipc.PreviewResponse, error) {
	panic("unexpected IPC ResolvePreview")
}
func (panicClient) RecordPreview(context.Context, sessionipc.PreviewRequest) error {
	panic("unexpected IPC RecordPreview")
}
func (panicClient) Display(context.Context) (sessionipc.DisplayResponse, error) {
	panic("unexpected IPC Display")
}

type shortWriter struct {
	data    []byte
	maximum int
}

func (writer *shortWriter) Write(data []byte) (int, error) {
	if len(data) > writer.maximum {
		data = data[:writer.maximum]
	}
	writer.data = append(writer.data, data...)
	return len(data), nil
}

func (writer *shortWriter) String() string { return string(writer.data) }

func (client *fakeClient) Event(_ context.Context, request sessionipc.EventRequest) (sessionipc.EventResponse, error) {
	client.events = append(client.events, request)
	return sessionipc.EventResponse{Effect: client.effect}, client.err
}
func (client *fakeClient) Load(_ context.Context, request sessionipc.LoadRequest) ([]byte, error) {
	client.loads = append(client.loads, request)
	return client.load, client.err
}
func (client *fakeClient) ResolvePreview(_ context.Context, request sessionipc.PreviewRequest) (sessionipc.PreviewResponse, error) {
	client.previews = append(client.previews, request)
	return client.resolved, client.err
}
func (client *fakeClient) RecordPreview(_ context.Context, request sessionipc.PreviewRequest) error {
	client.previews = append(client.previews, request)
	return client.recordErr
}
func (client *fakeClient) Display(context.Context) (sessionipc.DisplayResponse, error) {
	client.displays++
	return client.display, client.err
}

func TestEventReadsOnlyFZFEnvironmentAndWritesTypedEffect(t *testing.T) {
	client := &fakeClient{effect: protocol.Effect{Search: "off", Prompt: "[N] ", Header: "/very/long/path/"}}
	env := map[string]string{"FZF_KEY": "enter", "FZF_QUERY": "a b", "FZF_CURRENT_ITEM": "file\tdisplay\tYQ==", "FZF_COLUMNS": "12"}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(key string) string { return env[key] }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "e:en"), deps); err != nil {
		t.Fatal(err)
	}
	want := sessionipc.EventRequest{Opcode: protocol.OpEnter, Key: "enter", QueryBase64: "YSBi", CurrentItemBase64: "ZmlsZQlkaXNwbGF5CVlRPT0="}
	if len(client.events) != 1 || client.events[0] != want {
		t.Fatalf("events=%+v want=%+v", client.events, want)
	}
	if stdout.String() != "disable-search+change-prompt([N] )+change-header:··ath/" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestDisplayReadsIPCOnceAndWritesVisibleHeader(t *testing.T) {
	client := &fakeClient{display: sessionipc.DisplayResponse{Header: "/very/long/path/"}}
	env := map[string]string{"FZF_COLUMNS": "12"}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(key string) string { return env[key] }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "d"), deps); err != nil {
		t.Fatal(err)
	}
	if client.displays != 1 || stdout.String() != "change-header:··ath/" {
		t.Fatalf("displays=%d stdout=%q", client.displays, stdout.String())
	}
}

func TestInfoUsesNoIPCAndWritesCounts(t *testing.T) {
	env := map[string]string{"FZF_MATCH_COUNT": "7", "FZF_TOTAL_COUNT": "42", "FZF_SELECT_COUNT": "1"}
	var stdout bytes.Buffer
	deps := Dependencies{LookupEnv: func(key string) string { return env[key] }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "i:cp"), deps); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "7/42 (1)" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestEventInvalidDimensionsStillRendersRemainingEffect(t *testing.T) {
	client := &fakeClient{effect: protocol.Effect{Prompt: "[N] ", Header: "/work/", ReloadGeneration: 2}}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(key string) string {
		if key == "FZF_KEY" {
			return "enter"
		}
		return ""
	}, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "e:en"), deps); err != nil {
		t.Fatal(err)
	}
	want := "reload-sync(l:2)+wait+first+change-preview(p)+unbind(change,result-final)+change-prompt([N] )"
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestDispatchRejectsWrongEventKeyBeforeIPC(t *testing.T) {
	client := &fakeClient{}
	deps := Dependencies{Client: client, LookupEnv: func(key string) string {
		if key == "FZF_KEY" {
			return "$(id)"
		}
		return "ignored"
	}, Stdout: io.Discard, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "e:en"), deps); !errors.Is(err, ErrKey) {
		t.Fatalf("err=%v", err)
	}
	if len(client.events) != 0 {
		t.Fatal("IPC called for rejected key")
	}
}

func TestLocalEventValidationReadsOnlyFZFKey(t *testing.T) {
	var keys []string
	err := ValidateLocal(mustParse(t, "e:en"), func(key string) string {
		keys = append(keys, key)
		return "wrong"
	})
	if !errors.Is(err, ErrKey) || !reflect.DeepEqual(keys, []string{"FZF_KEY"}) {
		t.Fatalf("err=%v keys=%q", err, keys)
	}
}

func TestRestoreEventAllowsEditingKey(t *testing.T) {
	var keys []string
	err := ValidateLocal(Command{Kind: KindEvent, Opcode: protocol.OpRestoreView}, func(key string) string {
		keys = append(keys, key)
		return "backspace"
	})
	if err != nil || len(keys) != 0 {
		t.Fatalf("err=%v keys=%q", err, keys)
	}
}

func TestFixedLocalCallbacksNeedNoIPCOrFilesystem(t *testing.T) {
	for _, test := range []struct {
		command Command
		want    string
	}{
		{Command{Kind: KindEmptySource}, ""},
		{Command{Kind: KindInvalidPreview}, "[Invalid Path]"},
	} {
		var output bytes.Buffer
		err := Dispatch(context.Background(), test.command, Dependencies{
			Client: panicClient{}, LookupEnv: func(string) string { return "" }, Stdout: &output, Stderr: io.Discard,
			Preview: func(context.Context, protocol.ResolvedCandidate, io.Writer, io.Writer) error {
				panic("unexpected preview or filesystem work")
			},
		})
		if err != nil || output.String() != test.want {
			t.Fatalf("command=%+v output=%q err=%v", test.command, output.String(), err)
		}
	}
}

func TestLocalDisplayAndInfoValidationReadNoEnvironment(t *testing.T) {
	for _, raw := range []string{"d", "i:cd", "i:cp"} {
		var keys []string
		if err := ValidateLocal(mustParse(t, raw), func(key string) string {
			keys = append(keys, key)
			return "ignored"
		}); err != nil || len(keys) != 0 {
			t.Fatalf("command=%q err=%v keys=%q", raw, err, keys)
		}
	}
}

func TestLoadWritesExactOctets(t *testing.T) {
	client := &fakeClient{load: []byte{'a', 0, '\n', 0xff}}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "l:42"), deps); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), client.load) || !reflect.DeepEqual(client.loads, []sessionipc.LoadRequest{{Generation: 42}}) {
		t.Fatalf("output=%v loads=%+v", stdout.Bytes(), client.loads)
	}
}

func TestEventAndLoadWriteAllOrReturnShortWrite(t *testing.T) {
	t.Run("partial progress completes", func(t *testing.T) {
		client := &fakeClient{effect: protocol.Effect{Search: "off"}, load: []byte("load-data")}
		for _, command := range []string{"e:en", "l:1"} {
			writer := &shortWriter{maximum: 1}
			deps := Dependencies{Client: client, LookupEnv: func(key string) string {
				if key == "FZF_KEY" {
					return "enter"
				}
				return ""
			}, Stdout: writer, Stderr: io.Discard}
			if err := Dispatch(context.Background(), mustParse(t, command), deps); err != nil {
				t.Fatalf("Dispatch(%q): %v", command, err)
			}
			want := "disable-search"
			if command == "l:1" {
				want = "load-data"
			}
			if writer.String() != want {
				t.Fatalf("Dispatch(%q) wrote %q want %q", command, writer.String(), want)
			}
		}
	})
	t.Run("zero progress fails", func(t *testing.T) {
		client := &fakeClient{load: []byte("load-data")}
		writer := &shortWriter{maximum: 0}
		deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: writer, Stderr: io.Discard}
		if err := Dispatch(context.Background(), mustParse(t, "l:1"), deps); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPreviewResolvesAuthoritativePathAndRecordsBoundedTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	client := &fakeClient{resolved: sessionipc.PreviewResponse{Kind: protocol.KindFile,
		PathBase64: base64.StdEncoding.EncodeToString([]byte(path)), Size: 7, Mode: 0o600}}
	env := map[string]string{"FZF_QUERY": "ignored", "FZF_CURRENT_ITEM": "wire record"}
	var got protocol.ResolvedCandidate
	deps := Dependencies{Client: client, LookupEnv: func(key string) string { return env[key] }, Stdout: io.Discard, Stderr: io.Discard,
		Preview: func(_ context.Context, candidate protocol.ResolvedCandidate, _, _ io.Writer) error {
			got = candidate
			return nil
		}}
	if err := Dispatch(context.Background(), mustParse(t, "p"), deps); err != nil {
		t.Fatal(err)
	}
	if string(got.Path) != path || got.Kind != protocol.KindFile {
		t.Fatalf("candidate=%+v", got)
	}
	if len(client.previews) != 3 || client.previews[0].Phase != "resolve" || client.previews[1].Phase != "started" ||
		client.previews[2].Phase != "finished" || client.previews[2].Renderer != "native" ||
		client.previews[2].ChildStarts != 0 || client.previews[2].MaxLiveChildren != 0 {
		t.Fatalf("preview requests=%+v", client.previews)
	}
}

func TestPreviewRejectsVirtualAndRelativeBeforeRenderer(t *testing.T) {
	for _, response := range []sessionipc.PreviewResponse{
		{Kind: protocol.KindVirtual, PathBase64: base64.StdEncoding.EncodeToString([]byte("drives"))},
		{Kind: protocol.KindFile, PathBase64: base64.StdEncoding.EncodeToString([]byte("relative"))},
	} {
		client := &fakeClient{resolved: response}
		called := false
		deps := Dependencies{Client: client, LookupEnv: func(string) string { return "item" }, Stdout: io.Discard, Stderr: io.Discard,
			Preview: func(context.Context, protocol.ResolvedCandidate, io.Writer, io.Writer) error {
				called = true
				return nil
			}}
		if err := Dispatch(context.Background(), mustParse(t, "p"), deps); err == nil || called {
			t.Fatalf("err=%v called=%v", err, called)
		}
	}
}

func TestPreviewTelemetryFailureDoesNotReplaceRendererStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	renderErr := errors.New("render failed")
	client := &fakeClient{recordErr: context.DeadlineExceeded, resolved: sessionipc.PreviewResponse{
		Kind: protocol.KindFile, PathBase64: base64.StdEncoding.EncodeToString([]byte(path))}}
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "item" }, Stdout: io.Discard, Stderr: io.Discard,
		Preview: func(context.Context, protocol.ResolvedCandidate, io.Writer, io.Writer) error { return renderErr }}
	started := time.Now()
	err := Dispatch(context.Background(), mustParse(t, "p"), deps)
	if !errors.Is(err, renderErr) || time.Since(started) > time.Second {
		t.Fatalf("err=%v duration=%s", err, time.Since(started))
	}
}

func TestPreviewTerminalResourceSkipsFinishedTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	client := &fakeClient{resolved: sessionipc.PreviewResponse{Kind: protocol.KindFile,
		PathBase64: base64.StdEncoding.EncodeToString([]byte(path))}}
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "item" }, Stdout: io.Discard, Stderr: io.Discard,
		Preview: func(ctx context.Context, _ protocol.ResolvedCandidate, _, _ io.Writer) error {
			ObservePreviewDispatch(ctx, "bat", 101, 0)
			ObservePreviewProcess(ctx, processpkg.ProcessEvent{Phase: "start", PID: 101})
			ObservePreviewProcess(ctx, processpkg.ProcessEvent{Phase: "exit", PID: 101})
			return errors.Join(fmt.Errorf("renderer limit: %w", previewpkg.ErrTerminalResource),
				&processpkg.ExitError{Code: 17}, processpkg.ErrWaitDelay)
		}}
	err := Dispatch(context.Background(), mustParse(t, "p"), deps)
	var exitErr *processpkg.ExitError
	if !errors.Is(err, previewpkg.ErrTerminalResource) || !errors.Is(err, processpkg.ErrWaitDelay) ||
		!errors.As(err, &exitErr) || exitErr.ExitCode() != 17 || len(client.previews) != 2 ||
		client.previews[0].Phase != "resolve" || client.previews[1].Phase != "started" {
		t.Fatalf("err=%v previews=%+v", err, client.previews)
	}
}

func TestPreviewAggregatesSequentialChildTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	client := &fakeClient{resolved: sessionipc.PreviewResponse{Kind: protocol.KindFile,
		PathBase64: base64.StdEncoding.EncodeToString([]byte(path))}}
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "item" }, Stdout: io.Discard, Stderr: io.Discard,
		Preview: func(ctx context.Context, _ protocol.ResolvedCandidate, _, _ io.Writer) error {
			ObservePreviewDispatch(ctx, "bat", 101, 0)
			ObservePreviewProcess(ctx, processpkg.ProcessEvent{Phase: "start", PID: 101})
			ObservePreviewProcess(ctx, processpkg.ProcessEvent{Phase: "exit", PID: 101})
			return nil
		}}
	if err := Dispatch(context.Background(), mustParse(t, "p"), deps); err != nil {
		t.Fatal(err)
	}
	finished := client.previews[len(client.previews)-1]
	if finished.Renderer != "bat" || finished.ChildStarts != 1 || finished.MaxLiveChildren != 1 {
		t.Fatalf("finished=%+v", finished)
	}
}

func TestPreviewTelemetryDistinguishesNativeFallbackAfterChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	client := &fakeClient{resolved: sessionipc.PreviewResponse{Kind: protocol.KindFile,
		PathBase64: base64.StdEncoding.EncodeToString([]byte(path))}}
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "item" }, Stdout: io.Discard, Stderr: io.Discard,
		Preview: func(ctx context.Context, _ protocol.ResolvedCandidate, _, _ io.Writer) error {
			ObservePreviewDispatch(ctx, "bat", 101, 0)
			ObservePreviewProcess(ctx, processpkg.ProcessEvent{Phase: "start", PID: 101})
			ObservePreviewProcess(ctx, processpkg.ProcessEvent{Phase: "exit", PID: 101})
			ObservePreviewDispatch(ctx, "native", 0, 0)
			return nil
		}}
	if err := Dispatch(context.Background(), mustParse(t, "p"), deps); err != nil {
		t.Fatal(err)
	}
	finished := client.previews[len(client.previews)-1]
	if finished.Renderer != "bat-fallback" || finished.ChildStarts != 1 || finished.MaxLiveChildren != 1 {
		t.Fatalf("finished=%+v", finished)
	}
}

func mustParse(t *testing.T, raw string) Command {
	t.Helper()
	command, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return command
}
