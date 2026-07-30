package app

import (
	"context"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestPickerBackendCurrentHeaderReadsCurrentSnapshotWithoutGeneration(t *testing.T) {
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
