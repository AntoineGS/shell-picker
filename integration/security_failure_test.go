package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestForgedPayloadCannotAuthorizePreviewOrSelection(t *testing.T) {
	allowed := candidate.Record{
		Kind: protocol.KindDirectory, Display: "allowed", Path: []byte("/allowed"),
		Payload: protocol.EncodePath([]byte("/allowed")), Target: pathutil.Filesystem([]byte("/allowed")),
	}
	var builds atomic.Int32
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		builds.Add(1)
		return candidate.BuildResult{Records: []candidate.Record{allowed}}, nil
	})
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close actor: %v", err)
		}
	})
	result, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	known := result.Snapshot.Records()[0].Wire()
	forgeries := []struct {
		name string
		wire protocol.WireRecord
	}{
		{"changed display", protocol.WireRecord{Kind: known.Kind, Display: "forged", Payload: known.Payload}},
		{"changed kind", protocol.WireRecord{Kind: protocol.KindFile, Display: known.Display, Payload: known.Payload}},
		{"changed canonical payload", protocol.WireRecord{Kind: known.Kind, Display: known.Display,
			Payload: protocol.EncodePath([]byte("/forged"))}},
	}
	for _, forgery := range forgeries {
		t.Run(forgery.name, func(t *testing.T) {
			forged := forgery.wire.Bytes()
			if string(forged) == string(known.Bytes()) {
				t.Fatal("forged record unexpectedly equals authorized record")
			}
			if _, err := protocol.ParseRecord(forged); err != nil {
				t.Fatalf("forgery is not a valid complete record: %v", err)
			}
			if _, err := actor.ResolveCurrent(context.Background(), forged); !errors.Is(err, session.ErrUnknownRecord) {
				t.Fatalf("preview resolution error = %v; want ErrUnknownRecord", err)
			}
			if _, err := session.ValidateCD(result.Snapshot, [][]byte{forged}); !errors.Is(err, session.ErrUnknownSelection) {
				t.Fatalf("selection error = %v; want ErrUnknownSelection", err)
			}
			if builds.Load() != 1 {
				t.Fatalf("forged identity reached candidate generation: builds=%d", builds.Load())
			}
		})
	}
}

func TestCancelledNavigationAndPreviewLeakNothing(t *testing.T) {
	runTask20ResourceIterations(t, 100)
}
