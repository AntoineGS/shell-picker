package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestPickerBackendCurrentHeaderAllocationsDoNotScaleWithCandidateCount(t *testing.T) {
	small := currentHeaderAllocs(t, 1)
	large := currentHeaderAllocs(t, 10_000)
	if large > small+5 {
		t.Fatalf("CurrentHeader allocations scale with candidates: one=%.0f ten-thousand=%.0f", small, large)
	}
}

func currentHeaderAllocs(t *testing.T, recordCount int) float64 {
	t.Helper()
	records := make([]candidate.Record, recordCount)
	for index := range records {
		path := []byte("/candidate/" + strconv.Itoa(index))
		records[index] = candidate.Record{
			Kind: protocol.KindDirectory, Display: string(path), Path: path,
			Payload: protocol.EncodePath(path), Target: pathutil.Filesystem(path),
		}
	}
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: records}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	_, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte("/work")), Home: pathutil.Filesystem([]byte("/home"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &pickerBackend{actor: actor}
	return testing.AllocsPerRun(20, func() {
		if _, err := backend.CurrentHeader(context.Background()); err != nil {
			panic(err)
		}
	})
}

func TestPickerBackendCurrentHeaderReadsCurrentStateWithoutGenerationOrBuild(t *testing.T) {
	generatorCalls := 0
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		generatorCalls++
		return candidate.BuildResult{}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	result, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte("/work")), Home: pathutil.Filesystem([]byte("/work"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/work"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &pickerBackend{actor: actor}
	header, err := backend.CurrentHeader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if header != pathutil.PromptDisplayHome(pathutil.Filesystem([]byte("/work")), pathutil.Filesystem([]byte("/work"))) {
		t.Fatalf("header=%q", header)
	}
	snapshot, err := actor.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation() != result.Snapshot.Generation() || snapshot.Generation() != 1 {
		t.Fatalf("generation=%d applied=%d", snapshot.Generation(), result.Snapshot.Generation())
	}
	if generatorCalls != 1 {
		t.Fatalf("generator calls=%d want=1", generatorCalls)
	}
}

func TestPickerBackendCurrentHeaderEmitsDisplayTrace(t *testing.T) {
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCP, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte("/work")), Home: pathutil.Filesystem([]byte("/home"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCP, Location: pathutil.Filesystem([]byte("/work"))},
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	backend := &pickerBackend{actor: actor, trace: &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1})}}
	if _, err := backend.CurrentHeader(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := decodeBackendTraceRecords(t, output.String())
	if len(records) != 2 {
		t.Fatalf("display trace records=%d want 2; output=%q", len(records), output.Bytes())
	}
	if records[0].Event != "callback.display.start" || records[0].Outcome != "started" {
		t.Fatalf("display start trace=%+v", records[0])
	}
	if records[1].Event != "callback.display" || records[1].Outcome != "ok" {
		t.Fatalf("display completion trace=%+v, want callback.display/ok", records[1])
	}
	t.Run("error completion", testPickerBackendCurrentHeaderEmitsErrorCompletionTrace)
}

func testPickerBackendCurrentHeaderEmitsErrorCompletionTrace(t *testing.T) {
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCP, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte("/work")), Home: pathutil.Filesystem([]byte("/home"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCP, Location: pathutil.Filesystem([]byte("/work"))},
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	backend := &pickerBackend{actor: actor, trace: &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1})}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.CurrentHeader(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CurrentHeader error=%v, want context canceled", err)
	}
	records := decodeBackendTraceRecords(t, output.String())
	if len(records) != 2 {
		t.Fatalf("display error records=%d want 2; output=%q", len(records), output.Bytes())
	}
	if records[0].Event != "callback.display.start" || records[0].Outcome != "started" ||
		records[1].Event != "callback.display" || records[1].Outcome != "error" {
		t.Fatalf("display error traces=%+v", records)
	}
}

func decodeBackendTraceRecords(t *testing.T, raw string) []integrationpkg.TraceRecord {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	records := make([]integrationpkg.TraceRecord, 0, len(lines))
	for _, line := range lines {
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("trace record: %v; line=%q", err, line)
		}
		records = append(records, record)
	}
	return records
}
