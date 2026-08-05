package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestPickerTraceCreatesPrivateFileAndRecordsLifecycle(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.options.TracePath = filepath.Join(t.TempDir(), "picker.trace.jsonl")
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		for _, value := range append(append([]string(nil), config.Environment...), config.Options...) {
			if strings.Contains(value, fixture.options.TracePath) {
				t.Fatalf("trace path inherited by fzf/callback: %q", value)
			}
		}
		config.Runner.Observe(process.ProcessEvent{Phase: "start", PID: 42, Path: config.FZFPath})
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fixture.options.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("trace mode=%#o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(fixture.options.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var names []string
	for decoder.More() {
		var event integrationpkg.TraceRecord
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		names = append(names, event.Event)
	}
	want := []string{"session.start", "generation.start", "generation.publish", "fzf.start", "fzf.exit", "session.close"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("trace events=%q want %q; raw=%s", names, want, raw)
	}
	if bytes.Contains(raw, []byte(fixture.cwd)) || bytes.Contains(raw, []byte("SHELL_PICKER_TOKEN")) ||
		bytes.Contains(raw, []byte("query")) || bytes.Contains(raw, []byte("record")) {
		t.Fatalf("trace leaked sensitive value: %s", raw)
	}
	var publication integrationpkg.TraceRecord
	decoder = json.NewDecoder(bytes.NewReader(raw))
	for decoder.More() {
		var event integrationpkg.TraceRecord
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "generation.publish" {
			publication = event
		}
	}
	if publication.ZoxidePolicy != "cached" || publication.ZoxideAttempts != 0 || publication.ZoxideStarts != 0 ||
		publication.ZoxideMaxLive != 0 || publication.ZoxideOutcome != "not-run" || publication.LocalUS < 0 || publication.TransformUS < 0 {
		t.Fatalf("publication=%+v", publication)
	}
}

func TestPickerTraceWriteFailureReportsOnceThenRemainsSecondary(t *testing.T) {
	var diagnostic bytes.Buffer
	writer := &appFailingTraceWriter{}
	sink := &pickerTrace{trace: integrationpkg.NewTrace(writer, [16]byte{1}), diagnostic: &diagnostic}
	sink.event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"})
	sink.event(integrationpkg.TraceEvent{Name: "session.close", Outcome: "error"})
	if writer.calls != 1 || diagnostic.String() != "shell-picker: trace disabled\n" {
		t.Fatalf("calls=%d diagnostic=%q", writer.calls, diagnostic.String())
	}
}

func TestPickerTraceFinishKeepsOutcomeWhenCloseFails(t *testing.T) {
	var diagnostic bytes.Buffer
	sink := &failingTraceSink{closeErr: errors.New("close failed")}
	trace := &pickerTrace{trace: integrationpkg.NewTrace(sink, [16]byte{1}), sink: sink, diagnostic: &diagnostic}
	want := protocol.Outcome{Status: protocol.StatusAccepted, Paths: [][]byte{[]byte("accepted")}}

	got, err := trace.finish(want, nil)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("finish outcome=%+v err=%v want=%+v", got, err, want)
	}
	if diagnostic.String() != "shell-picker: trace disabled\n" {
		t.Fatalf("diagnostic=%q", diagnostic.String())
	}
	if !bytes.Contains(sink.Bytes(), []byte(`"event":"session.close"`)) ||
		!bytes.Contains(sink.Bytes(), []byte(`"outcome":"accepted"`)) {
		t.Fatalf("trace=%s", sink.Bytes())
	}
}

func TestPickerTraceFinishKeepsPrimaryErrorWhenSessionCloseWriteFails(t *testing.T) {
	var diagnostic bytes.Buffer
	sink := &failingTraceSink{writeErr: errors.New("write failed")}
	trace := &pickerTrace{trace: integrationpkg.NewTrace(sink, [16]byte{1}), sink: sink, diagnostic: &diagnostic}
	primary := errors.New("picker failed")

	got, err := trace.finish(protocol.Outcome{}, primary)
	if !reflect.DeepEqual(got, protocol.Outcome{}) || !errors.Is(err, primary) {
		t.Fatalf("finish outcome=%+v err=%v", got, err)
	}
	if sink.closeCalls != 1 || diagnostic.String() != "shell-picker: trace disabled\n" {
		t.Fatalf("close calls=%d diagnostic=%q", sink.closeCalls, diagnostic.String())
	}
}

type failingTraceSink struct {
	bytes.Buffer
	writeErr   error
	closeErr   error
	closeCalls int
}

func (sink *failingTraceSink) Write(data []byte) (int, error) {
	if sink.writeErr != nil {
		return 0, sink.writeErr
	}
	return sink.Buffer.Write(data)
}

func (sink *failingTraceSink) Close() error {
	sink.closeCalls++
	return sink.closeErr
}

type appFailingTraceWriter struct{ calls int }

func (writer *appFailingTraceWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, errors.New("fixture write failure")
}
