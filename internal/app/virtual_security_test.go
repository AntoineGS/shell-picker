package app

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestPickerBackendRejectsAuthorizedVirtualBeforeFilesystemAndOutput(t *testing.T) {
	virtual := candidate.Record{Kind: protocol.KindVirtual, Display: "..", Path: []byte(protocol.VirtualDrivesTarget),
		Payload: protocol.EncodePath([]byte(protocol.VirtualDrivesTarget)), Target: pathutil.Drives()}
	ordinary := candidate.Record{Kind: protocol.KindFile, Display: "file", Path: []byte("/file"),
		Payload: protocol.EncodePath([]byte("/file")), Target: pathutil.Filesystem([]byte("/file"))}
	actor := session.New(context.Background(), func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if request.Location.Kind == pathutil.KindDrives {
			return candidate.BuildResult{}, nil
		}
		return candidate.BuildResult{Records: []candidate.Record{ordinary, virtual}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	initial, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte("/")), Home: pathutil.Filesystem([]byte("/"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	virtualWire := virtual.Wire().Bytes()
	resolved, err := actor.ResolveCurrent(context.Background(), virtualWire)
	if err != nil || resolved.Target.Kind != pathutil.KindDrives {
		t.Fatalf("authorized virtual membership resolution failed: target=%v err=%v", resolved.Target.Kind, err)
	}
	backend := &pickerBackend{actor: actor, metrics: &pickerMetrics{}}
	if _, err := backend.ResolvePreview(context.Background(), virtualWire); !errors.Is(err, session.ErrUnknownRecord) {
		t.Fatalf("virtual preview error=%T %v; want session semantic rejection", err, err)
	} else {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			t.Fatalf("virtual preview reached filesystem stat")
		}
	}
	if outcome, err := session.ValidateCD(initial.Snapshot, [][]byte{virtualWire}); !errors.Is(err, session.ErrInvalidSelection) || outcome.Status != "" || len(outcome.Paths) != 0 {
		t.Fatalf("virtual CD outcome=%+v err=%v", outcome, err)
	}
	if outcome, err := session.ValidateCP(initial.Snapshot, [][]byte{ordinary.Wire().Bytes(), virtualWire}, []byte("/")); !errors.Is(err, session.ErrInvalidSelection) || outcome.Status != "" || len(outcome.Paths) != 0 {
		t.Fatalf("mixed virtual CP outcome=%+v err=%v", outcome, err)
	}
	navigated, err := session.Handle(context.Background(), actor,
		protocol.Event{Opcode: protocol.OpForward, CurrentItem: virtualWire})
	if err != nil || navigated.Snapshot.State().Location.Kind != pathutil.KindDrives {
		t.Fatalf("authorized virtual navigation location=%v err=%v", navigated.Snapshot.State().Location.Kind, err)
	}
}
