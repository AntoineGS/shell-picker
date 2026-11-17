//go:build !windows

package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestCompletionEditingReanchorsAndRestoresZoxide(t *testing.T) {
	ctx := context.Background()
	local := eventRecord(protocol.KindLocal, "project/", "/work/project")
	frequent := eventRecord(protocol.KindZoxide, "/frequent/", "/frequent")
	actor := New(ctx, func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: []candidate.Record{
			eventRecord(protocol.KindDirectory, "child/", string(request.Location.Path)+"/child"),
		}}, nil
	})
	defer actor.Close()
	initial := eventSnapshot(protocol.PickerCD, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), local, frequent)
	initial.state.Completion = &CompletionContext{Base: "/work"}
	_, err := actor.Apply(ctx, ProposedTransition{State: initial.state,
		Build: &candidate.BuildRequest{}, restoreRecords: initial.records})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ query, directory, search string }{
		{"~", "/home/test", ""},
		{"~/notes/to", "/home/test/notes", "to"},
		{"../../..", "/", ""},
		{"/usr/lo", "/usr", "lo"},
		{"", "/work", ""},
	} {
		result, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpQuery, Query: []byte(test.query)})
		if err != nil {
			t.Fatal(err)
		}
		state := result.Snapshot.State()
		if string(state.Location.Path) != test.directory || string(state.Query) != test.query ||
			result.Effect.ClearQuery || !result.Effect.SetSearchQuery || result.Effect.SearchQuery != test.search {
			t.Fatalf("query=%q state=%+v effect=%+v", test.query, state, result.Effect)
		}
		if test.query == "" && result.Snapshot.RecordCount() != 2 {
			t.Fatalf("returning to launch directory lost local/zoxide candidates: %+v", result.Snapshot.Records())
		}
	}
}

func TestCompletionForwardPreservesEditablePrefix(t *testing.T) {
	directory := eventRecord(protocol.KindDirectory, "project/", "/work/project")
	for _, query := range []string{"../pro", "../", "../../work/pro"} {
		snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), directory)
		snapshot.state.Completion = &CompletionContext{Base: "/work/start"}
		reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpForward,
			Query: []byte(query), CurrentItem: []byte(directory.FullKey())})
		if err != nil {
			t.Fatal(err)
		}
		proposal := reduction.proposalForTest()
		want := "../project/"
		if query == "../../work/pro" {
			want = "../../work/project/"
		}
		if proposal.Effect.Query != want || !proposal.Effect.SetQuery || proposal.Effect.ClearQuery ||
			string(proposal.State.Location.Path) != "/work/project" {
			t.Fatalf("query=%q proposal=%+v effect=%+v", query, proposal, proposal.Effect)
		}
	}
}

func TestCompletionSpecialKeysAreTextAndEmptyForwardIsHarmless(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")))
	snapshot.state.Completion = &CompletionContext{Base: "/work"}
	for _, test := range []struct {
		op  protocol.Opcode
		put string
	}{
		{protocol.OpSlash, "/"}, {protocol.OpHome, "~"}, {protocol.OpForward, ""},
	} {
		reduction, err := Reduce(snapshot, protocol.Event{Opcode: test.op})
		if err != nil {
			t.Fatal(err)
		}
		proposal := reduction.proposalForTest()
		if proposal.Build != nil || proposal.Effect.Put != test.put || proposal.Effect.ClearQuery {
			t.Fatalf("%s unexpectedly navigated/cleared: %+v", test.op, proposal)
		}
	}
}

func TestCompletionCancelAddRestoresBrowserPath(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeAdd, pathutil.Filesystem([]byte("/work/child")))
	snapshot.state.Completion = &CompletionContext{Base: "/work"}
	snapshot.state.Browser = &BrowserContext{Query: []byte("child/")}
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpEscape, Query: []byte("unfinished")})
	if err != nil {
		t.Fatal(err)
	}
	proposal := reduction.proposalForTest()
	if proposal.Effect.ClearQuery || !proposal.Effect.SetQuery || proposal.Effect.Query != "child/" {
		t.Fatalf("lost browser path on Add cancellation: %+v", proposal.Effect)
	}
}

func TestCompletionCreatedDirectoryStaysInEditablePath(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actor := New(ctx, func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, nil
	})
	defer actor.Close()
	state := State{Picker: protocol.PickerCP, Mode: protocol.ModeAdd, Location: pathutil.Filesystem([]byte(child)),
		Home: pathutil.Filesystem([]byte(base)), Completion: &CompletionContext{Base: base},
		Browser: &BrowserContext{Query: []byte("child/")}}
	if _, err := actor.Apply(ctx, ProposedTransition{State: state}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("created")})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Effect.Completion || !result.Effect.SetQuery || result.Effect.ClearQuery ||
		result.Effect.Query != "child/created/" || string(result.Snapshot.State().Query) != "child/created/" {
		t.Fatalf("created directory lost completion path: state=%+v effect=%+v", result.Snapshot.State(), result.Effect)
	}
	if _, err := os.Stat(filepath.Join(child, "created")); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionIndexedDescentSavesSearchQuery(t *testing.T) {
	directory := eventRecord(protocol.KindDirectory, "/work/child", "/work/child")
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), directory)
	snapshot.state.Source = protocol.SourceIndexed
	snapshot.state.Indexed = &IndexedContext{Query: []byte("old")}
	snapshot.state.Completion = &CompletionContext{Base: "/work"}
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpForward, Query: []byte("child"),
		CurrentItem: []byte(directory.FullKey())})
	if err != nil {
		t.Fatal(err)
	}
	state := reduction.proposalForTest().State
	if state.Source != protocol.SourceBrowser || string(state.Indexed.Query) != "child" || string(state.Query) != "child/" {
		t.Fatalf("lost source context: %+v indexed=%+v", state, state.Indexed)
	}
}
