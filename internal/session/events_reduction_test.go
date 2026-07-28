package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestReduceValidAddIsPureAndClonesIntent(t *testing.T) {
	base := t.TempDir()
	blockingFile := filepath.Join(base, "existing-file")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeAdd, pathutil.Filesystem([]byte(base)))
	event := protocol.Event{Opcode: protocol.OpEnter, Query: []byte("existing-file/child")}

	reduction, err := Reduce(snapshot, event)
	if err != nil || !reduction.hasAddIntent() || reduction.hasProposal() {
		t.Fatalf("reduction=%+v err=%v", reduction, err)
	}
	event.Query[0] = 'X'
	intent := reduction.addIntentForTest()
	intent.query[0] = 'Y'
	intent.base.Path[0] = 'Z'
	again := reduction.addIntentForTest()
	if string(again.query) != "existing-file/child" || string(again.base.Path) != base {
		t.Fatalf("intent=%+v", again)
	}

	discarded, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("missing/child")})
	if err != nil || !discarded.hasAddIntent() {
		t.Fatalf("discarded=%+v err=%v", discarded, err)
	}
	if _, err := os.Lstat(filepath.Join(base, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Reduce inspected or created missing path: %v", err)
	}
	contents, err := os.ReadFile(blockingFile)
	if err != nil || string(contents) != "x" {
		t.Fatalf("blocking file changed: contents=%q err=%v", contents, err)
	}
}

func TestNormalEscapeHasOnlyClearMultiEffect(t *testing.T) {
	actor := New(context.Background(), nil)
	t.Cleanup(func() { _ = actor.Close() })
	state := eventSnapshot(protocol.PickerCP, protocol.ModeNormal, pathutil.Filesystem([]byte("/work"))).state
	if _, err := actor.Apply(context.Background(), ProposedTransition{BaseGeneration: 0, State: state}); err != nil {
		t.Fatal(err)
	}
	before, err := actor.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEscape})
	if err != nil {
		t.Fatal(err)
	}
	if result.Effect != (protocol.Effect{ClearMulti: true}) {
		t.Fatalf("effect=%+v", result.Effect)
	}
	if !reflect.DeepEqual(result.Snapshot.State(), before.State()) {
		t.Fatalf("state=%+v want=%+v", result.Snapshot.State(), before.State())
	}
}

func TestEnterRejectsUnknownNonemptyCurrentItem(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")))
	unknown := eventRecord(protocol.KindDirectory, "unknown", "/work/unknown")
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpEnter, CurrentItem: []byte(unknown.FullKey())})
	if !errors.Is(err, ErrUnknownRecord) || reduction.hasProposal() || reduction.hasAddIntent() {
		t.Fatalf("reduction=%+v err=%v", reduction, err)
	}
}
