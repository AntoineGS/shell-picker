package app

import (
	"context"
	"strconv"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
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
	if header != pathutil.PromptDisplay(pathutil.Filesystem([]byte("/work"))) {
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
