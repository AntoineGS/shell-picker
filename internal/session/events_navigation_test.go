package session

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestPickerNavigationPreservesInsertAndNormalMode(t *testing.T) {
	directory := eventRecord(protocol.KindLocal, "Child", "/work/Child")
	for _, mode := range []protocol.Mode{protocol.ModeInsert, protocol.ModeNormal} {
		t.Run(string(mode), func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCD, mode, pathutil.Filesystem([]byte("/work")), directory)
			reduction, err := Reduce(snapshot, protocol.Event{
				Opcode: protocol.OpForward, CurrentItem: []byte(directory.FullKey()),
			})
			if err != nil {
				t.Fatal(err)
			}
			proposal := reduction.proposalForTest()
			if proposal.State.Mode != mode || proposal.Effect.Mode != mode || proposal.Effect.ClearQuery != true {
				t.Fatalf("proposal=%+v", proposal)
			}
		})
	}
}

func TestPickerExactSlashChoosesFirstCaseInsensitiveImmediateChild(t *testing.T) {
	first := eventRecord(protocol.KindLocal, "foo", "/work/foo")
	second := eventRecord(protocol.KindLocal, "Foo", "/work/Foo")
	snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), first, second)
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("FOO")})
	proposal := reduction.proposalForTest()
	if err != nil || string(proposal.State.Location.Path) != sessionTestPath("/work/foo") || proposal.State.Mode != protocol.ModeInsert {
		t.Fatalf("proposal=%+v err=%v", proposal, err)
	}
}

func TestPickerInvalidSlashRequestsFixedInvalidView(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")),
		eventRecord(protocol.KindDirectory, "foobar/", "/work/foobar"))
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("foo")})
	proposal := reduction.proposalForTest()
	want := protocol.Effect{Put: "/", InvalidPath: true}
	if err != nil || proposal.Effect != want || proposal.Build != nil {
		t.Fatalf("effect=%+v build=%+v err=%v", proposal.Effect, proposal.Build, err)
	}
}

func TestPickerExactImmediateChildExcludesIneligibleRecords(t *testing.T) {
	tests := []struct {
		name     string
		location pathutil.Location
		query    []byte
		record   candidate.Record
	}{
		{name: "dot", location: pathutil.Filesystem([]byte("/work")), query: []byte("."), record: eventRecord(protocol.KindLocal, ".", "/work")},
		{name: "dotdot", location: pathutil.Filesystem([]byte("/work")), query: []byte(".."), record: eventRecord(protocol.KindLocal, "..", "/")},
		{name: "zoxide", location: pathutil.Filesystem([]byte("/work")), query: []byte("foo"), record: eventRecord(protocol.KindZoxide, "foo", "/work/foo")},
		{name: "virtual", location: pathutil.Filesystem([]byte("/work")), query: []byte("foo"), record: eventRecord(protocol.KindVirtual, "foo", "/work/foo")},
		{name: "non-filesystem target", location: pathutil.Filesystem([]byte("/work")), query: []byte("foo"), record: func() candidate.Record {
			record := eventRecord(protocol.KindLocal, "foo", "/work/foo")
			record.Target = pathutil.Drives()
			return record
		}()},
		{name: "non-filesystem location", location: pathutil.Drives(), query: []byte("foo"), record: eventRecord(protocol.KindLocal, "foo", "/work/foo")},
		{name: "non-child", location: pathutil.Filesystem([]byte("/work")), query: []byte("foo"), record: eventRecord(protocol.KindLocal, "foo", "/other/foo")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeInsert, test.location, test.record)
			if record, ok := exactImmediateChild(snapshot, test.query); ok {
				t.Fatalf("record=%+v", record)
			}
		})
	}
}

func TestPickerExactImmediateChildUsesTargetBasename(t *testing.T) {
	record := eventRecord(protocol.KindDirectory, "alias/", "/work/actual")
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), record)
	if _, ok := exactImmediateChild(snapshot, []byte("alias")); ok {
		t.Fatal("matched display instead of target basename")
	}
	got, ok := exactImmediateChild(snapshot, []byte("ACTUAL"))
	if !ok || !bytes.Equal(got.Target.Path, []byte(sessionTestPath("/work/actual"))) {
		t.Fatalf("record=%+v ok=%t", got, ok)
	}
}

func TestPickerVirtualEnterPreservesInsertMode(t *testing.T) {
	virtual := eventRecord(protocol.KindVirtual, "Drives", "ignored")
	snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), virtual)
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpEnter, CurrentItem: []byte(virtual.FullKey())})
	proposal := reduction.proposalForTest()
	if err != nil || proposal.State.Mode != protocol.ModeInsert || proposal.Effect.Mode != protocol.ModeInsert {
		t.Fatalf("proposal=%+v err=%v", proposal, err)
	}
}

func TestPickerAddNavigationReturnsToNormal(t *testing.T) {
	root := t.TempDir()
	actor := New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	state := eventSnapshot(protocol.PickerCP, protocol.ModeAdd, pathutil.Filesystem([]byte(root))).state
	if _, err := actor.Apply(context.Background(), ProposedTransition{BaseGeneration: 0, State: state}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new")})
	if err != nil || result.Snapshot.State().Mode != protocol.ModeNormal || result.Effect.Mode != protocol.ModeNormal {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPickerSlashSpecialCasesPreserveMode(t *testing.T) {
	tests := []struct {
		name  string
		mode  protocol.Mode
		query []byte
		want  []byte
	}{
		{name: "insert empty", mode: protocol.ModeInsert, want: sessionTestRootPath()},
		{name: "normal empty", mode: protocol.ModeNormal, want: sessionTestRootPath()},
		{name: "insert exact dotdot", mode: protocol.ModeInsert, query: []byte(".."), want: []byte(sessionTestPath("/work"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCD, test.mode, pathutil.Filesystem([]byte("/work/child")))
			reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpSlash, Query: test.query})
			proposal := reduction.proposalForTest()
			if err != nil || proposal.State.Mode != test.mode || !bytes.Equal(proposal.State.Location.Path, test.want) {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
		})
	}
}

func TestPickerRestoreViewReturnsCurrentGenerationWithoutStateMutation(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")))
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpRestoreView})
	proposal := reduction.proposalForTest()
	if err != nil || proposal.Effect != (protocol.Effect{RestoreGeneration: 7}) || !reflect.DeepEqual(proposal.State, snapshot.state) || proposal.Build != nil {
		t.Fatalf("proposal=%+v err=%v", proposal, err)
	}
}
