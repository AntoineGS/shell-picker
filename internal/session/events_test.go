package session

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func eventRecord(kind protocol.Kind, display, path string) candidate.Record {
	target := pathutil.Filesystem([]byte(path))
	payload := protocol.EncodePath([]byte(path))
	if kind == protocol.KindVirtual {
		target = pathutil.Drives()
		payload = protocol.EncodePath([]byte(protocol.VirtualDrivesTarget))
	}
	return candidate.Record{Kind: kind, Display: display, Path: []byte(path), Payload: payload, Target: target}
}

func eventSnapshot(picker protocol.Picker, mode protocol.Mode, location pathutil.Location, records ...candidate.Record) Snapshot {
	prefix := "[I] "
	if mode == protocol.ModeNormal {
		prefix = "[N] "
	} else if mode == protocol.ModeAdd {
		prefix = "[A] "
	}
	state := State{
		Picker: picker, Mode: mode, Location: location,
		Home: pathutil.Filesystem([]byte("/home/test")), Prompt: prefix,
	}
	return Snapshot{generation: 7, state: state, records: records, byFullRecord: buildIndex(records)}
}

func TestModeAndQueryTransitionMatrix(t *testing.T) {
	record := eventRecord(protocol.KindDirectory, "child", "/work/child")
	tests := []struct {
		name  string
		mode  protocol.Mode
		event protocol.Event
		want  protocol.Effect
	}{
		{"normal i", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpModeInsert}, protocol.Effect{Mode: protocol.ModeInsert, Prompt: "[I] ", Search: "on", Rebind: protocol.ModeInsert, Cursor: protocol.CursorLine}},
		{"normal a", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpModeAdd}, protocol.Effect{Mode: protocol.ModeAdd, Prompt: "[A] ", Search: "on", Rebind: protocol.ModeAdd, ClearQuery: true, Cursor: protocol.CursorLine}},
		{"insert escape", protocol.ModeInsert, protocol.Event{Opcode: protocol.OpEscape}, protocol.Effect{Mode: protocol.ModeNormal, Prompt: "[N] ", Search: "off", Rebind: protocol.ModeNormal, Cursor: protocol.CursorBlock}},
		{"normal escape", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpEscape}, protocol.Effect{ClearMulti: true}},
		{"add escape", protocol.ModeAdd, protocol.Event{Opcode: protocol.OpEscape}, protocol.Effect{Mode: protocol.ModeNormal, Prompt: "[N] ", Search: "off", Rebind: protocol.ModeNormal, ClearQuery: true, Cursor: protocol.CursorBlock}},
		{"add forward", protocol.ModeAdd, protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(record.FullKey())}, protocol.Effect{Ignore: true}},
		{"add parent", protocol.ModeAdd, protocol.Event{Opcode: protocol.OpParent}, protocol.Effect{Ignore: true}},
		{"add slash", protocol.ModeAdd, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("..")}, protocol.Effect{Put: "/"}},
		{"add home", protocol.ModeAdd, protocol.Event{Opcode: protocol.OpHome, Query: []byte("x")}, protocol.Effect{Put: "~"}},
		{"normal slash query", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("name")}, protocol.Effect{Ignore: true}},
		{"normal home query", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpHome, Query: []byte("name")}, protocol.Effect{Ignore: true}},
		{"insert slash query", protocol.ModeInsert, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("name")}, protocol.Effect{Put: "/"}},
		{"insert home query", protocol.ModeInsert, protocol.Event{Opcode: protocol.OpHome, Query: []byte("name")}, protocol.Effect{Put: "~"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reduction, err := Reduce(eventSnapshot(protocol.PickerCP, test.mode, pathutil.Filesystem([]byte("/work")), record), test.event)
			if err != nil {
				t.Fatalf("Reduce() error = %v", err)
			}
			proposal := reduction.proposalForTest()
			if proposal.Effect != test.want {
				t.Fatalf("effect = %+v; want %+v", proposal.Effect, test.want)
			}
		})
	}
}

func TestModeEventsRejectBindingsUnavailableInCurrentMode(t *testing.T) {
	for _, test := range []struct {
		mode   protocol.Mode
		opcode protocol.Opcode
	}{{protocol.ModeInsert, protocol.OpModeInsert}, {protocol.ModeAdd, protocol.OpModeInsert}, {protocol.ModeInsert, protocol.OpModeAdd}, {protocol.ModeAdd, protocol.OpModeAdd}} {
		_, err := Reduce(eventSnapshot(protocol.PickerCD, test.mode, pathutil.Filesystem([]byte("/work"))), protocol.Event{Opcode: test.opcode})
		if !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("mode=%s opcode=%s error=%v", test.mode, test.opcode, err)
		}
	}
}

func TestModePromptContainsOnlyMode(t *testing.T) {
	for _, test := range []struct {
		mode     protocol.Mode
		addError bool
		want     string
	}{
		{protocol.ModeInsert, false, "[I] "},
		{protocol.ModeNormal, false, "[N] "},
		{protocol.ModeAdd, false, "[A] "},
		{protocol.ModeAdd, true, "[A!] "},
	} {
		if got := modePrompt(test.mode, test.addError); got != test.want {
			t.Fatalf("modePrompt(%q, %t)=%q want %q", test.mode, test.addError, got, test.want)
		}
	}
}

func TestNavigationEffectSeparatesPromptAndHeader(t *testing.T) {
	s := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")))
	reduction, err := Reduce(s, protocol.Event{Opcode: protocol.OpParent})
	proposal := reduction.proposalForTest()
	if err != nil || proposal.Effect.Prompt != "[N] " || proposal.Effect.Header != "/" {
		t.Fatalf("effect=%+v err=%v", proposal.Effect, err)
	}
}

func TestAcceptanceAndVirtualEnter(t *testing.T) {
	virtual := eventRecord(protocol.KindVirtual, "Drives", "ignored")
	for _, mode := range []protocol.Mode{protocol.ModeInsert, protocol.ModeNormal} {
		t.Run(string(mode)+" accepts filesystem", func(t *testing.T) {
			s := eventSnapshot(protocol.PickerCD, mode, pathutil.Filesystem([]byte("/work")))
			reduction, err := Reduce(s, protocol.Event{Opcode: protocol.OpEnter})
			proposal := reduction.proposalForTest()
			if err != nil || !proposal.Effect.Accept || proposal.Effect.ClearMulti {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
		})
		t.Run(string(mode)+" virtual navigates", func(t *testing.T) {
			s := eventSnapshot(protocol.PickerCD, mode, pathutil.Filesystem([]byte("/work")), virtual)
			reduction, err := Reduce(s, protocol.Event{Opcode: protocol.OpEnter, CurrentItem: []byte(virtual.FullKey())})
			proposal := reduction.proposalForTest()
			if err != nil || proposal.Effect.Accept || proposal.Build == nil || proposal.State.Location.Kind != pathutil.KindDrives {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
		})
	}
}

func TestVirtualNavigationRequiresAuthoritativeDrivesTarget(t *testing.T) {
	virtual := eventRecord(protocol.KindVirtual, "Drives", "ignored")
	virtual.Target = pathutil.Filesystem([]byte("/forged"))
	s := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")), virtual)
	for _, opcode := range []protocol.Opcode{protocol.OpForward, protocol.OpEnter} {
		_, err := Reduce(s, protocol.Event{Opcode: opcode, CurrentItem: []byte(virtual.FullKey())})
		if !errors.Is(err, ErrInvalidNavigation) {
			t.Fatalf("opcode=%s error=%v", opcode, err)
		}
	}
}

func TestFilesystemNavigationRejectsNonFilesystemTarget(t *testing.T) {
	directory := eventRecord(protocol.KindDirectory, "directory", "/work/directory")
	directory.Target = pathutil.Drives()
	s := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")), directory)
	_, err := Reduce(s, protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(directory.FullKey())})
	if !errors.Is(err, ErrInvalidNavigation) {
		t.Fatalf("error=%v", err)
	}
}

func TestNavigationTargetsKindsAndEffects(t *testing.T) {
	kinds := []protocol.Kind{protocol.KindLocal, protocol.KindDirectory, protocol.KindZoxide, protocol.KindDrive, protocol.KindVirtual}
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		for _, kind := range kinds {
			t.Run(string(picker)+"/"+string(kind), func(t *testing.T) {
				record := eventRecord(kind, string(kind), "/next")
				s := eventSnapshot(picker, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), record)
				reduction, err := Reduce(s, protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(record.FullKey())})
				if picker == protocol.PickerCP && (kind == protocol.KindLocal || kind == protocol.KindZoxide) {
					if !errors.Is(err, ErrInvalidNavigation) {
						t.Fatalf("Reduce() error = %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Reduce() error = %v", err)
				}
				proposal := reduction.proposalForTest()
				wantKind, wantPath := pathutil.KindFilesystem, "/next"
				if kind == protocol.KindVirtual {
					wantKind, wantPath = pathutil.KindDrives, ""
				}
				if proposal.State.Location.Kind != wantKind || string(proposal.State.Location.Path) != wantPath || proposal.Build == nil ||
					!proposal.Effect.ClearMulti || !proposal.Effect.ClearQuery || proposal.Effect.Cursor != protocol.CursorBlock {
					t.Fatalf("proposal=%+v", proposal)
				}
			})
		}
	}

	file := eventRecord(protocol.KindFile, "file", "/work/file")
	for _, test := range []struct {
		name   string
		picker protocol.Picker
		raw    []byte
	}{
		{"empty", protocol.PickerCD, nil},
		{"unknown", protocol.PickerCD, []byte(eventRecord(protocol.KindDirectory, "forged", "/next").FullKey())},
		{"cp file", protocol.PickerCP, []byte(file.FullKey())},
	} {
		s := eventSnapshot(test.picker, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")), file)
		if _, err := Reduce(s, protocol.Event{Opcode: protocol.OpForward, CurrentItem: test.raw}); !errors.Is(err, ErrInvalidNavigation) {
			t.Fatalf("%s error=%v", test.name, err)
		}
	}
}

func TestRootParentHomeNavigation(t *testing.T) {
	home := pathutil.Filesystem([]byte("/home/test"))
	tests := []struct {
		name  string
		mode  protocol.Mode
		event protocol.Event
		want  pathutil.Location
	}{
		{"insert slash empty", protocol.ModeInsert, protocol.Event{Opcode: protocol.OpSlash}, pathutil.Root()},
		{"normal slash empty", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpSlash}, pathutil.Root()},
		{"insert exact dotdot", protocol.ModeInsert, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("..")}, pathutil.Parent(pathutil.Filesystem([]byte("/work/child")))},
		{"parent", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpParent}, pathutil.Parent(pathutil.Filesystem([]byte("/work/child")))},
		{"home", protocol.ModeNormal, protocol.Event{Opcode: protocol.OpHome}, home},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := eventSnapshot(protocol.PickerCD, test.mode, pathutil.Filesystem([]byte("/work/child")))
			reduction, err := Reduce(s, test.event)
			proposal := reduction.proposalForTest()
			if err != nil || proposal.State.Location.Kind != test.want.Kind || !bytes.Equal(proposal.State.Location.Path, test.want.Path) || proposal.Build == nil {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
		})
	}
}

func TestAddSuccessInvalidAndBuildFailureRollback(t *testing.T) {
	root := t.TempDir()
	preexisting := filepath.Join(root, "existing")
	if err := os.Mkdir(preexisting, 0o755); err != nil {
		t.Fatal(err)
	}

	s := eventSnapshot(protocol.PickerCP, protocol.ModeAdd, pathutil.Filesystem([]byte(root)))
	reduction, err := Reduce(s, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("existing/new/leaf")})
	if err != nil || !reduction.hasAddIntent() || reduction.hasProposal() {
		t.Fatalf("reduction=%+v err=%v", reduction, err)
	}

	for _, query := range [][]byte{nil, []byte("../escape"), []byte("/absolute")} {
		badReduction, reduceErr := Reduce(s, protocol.Event{Opcode: protocol.OpEnter, Query: query})
		bad := badReduction.proposalForTest()
		wantEffect := protocol.Effect{Prompt: "[A!] ", ErrorPrompt: true}
		if reduceErr != nil || bad.State.Mode != protocol.ModeAdd || !bad.State.AddError || bad.Build != nil || bad.Effect != wantEffect {
			t.Fatalf("query=%q proposal=%+v err=%v", query, bad, reduceErr)
		}
	}
	drives := eventSnapshot(protocol.PickerCP, protocol.ModeAdd, pathutil.Drives())
	badReduction, err := Reduce(drives, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new")})
	bad := badReduction.proposalForTest()
	if err != nil || !bad.State.AddError || bad.State.Location.Kind != pathutil.KindDrives {
		t.Fatalf("Drives proposal=%+v err=%v", bad, err)
	}

	failing := New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, fs.ErrPermission
	})
	t.Cleanup(func() { _ = failing.Close() })
	if _, err := failing.Apply(context.Background(), ProposedTransition{BaseGeneration: 0, State: s.state}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), failing, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("failed")})
	if !errors.Is(err, fs.ErrPermission) || result.Effect != (protocol.Effect{}) {
		t.Fatalf("Handle() result=%+v err=%v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "failed")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("failed Add was not rolled back: %v", statErr)
	}
}

func TestAddRejectsSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	s := eventSnapshot(protocol.PickerCD, protocol.ModeAdd, pathutil.Filesystem([]byte(root)))
	reduction, err := Reduce(s, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("link/child")})
	if err != nil || !reduction.hasAddIntent() {
		t.Fatalf("reduction=%+v err=%v", reduction, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("created through symlink: %v", err)
	}
}

func TestHandleWaitsForPublishedGeneration(t *testing.T) {
	record := eventRecord(protocol.KindDirectory, "next", "/next")
	actor := New(context.Background(), func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: []candidate.Record{record}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	initial := eventSnapshot(protocol.PickerCD, protocol.ModeInsert, pathutil.Filesystem([]byte("/work")), record)
	if _, err := actor.Apply(context.Background(), ProposedTransition{BaseGeneration: 0, State: initial.state, Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: initial.state.Location}}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(record.FullKey())})
	if err != nil || result.Snapshot.Generation() != 2 || result.Effect.ReloadGeneration != 2 || len(result.Snapshot.Records()) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestHandleAddPublishesAndKeepsCreatedDirectories(t *testing.T) {
	root := t.TempDir()
	actor := New(context.Background(), func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: []candidate.Record{eventRecord(protocol.KindDirectory, "child", string(request.Location.Path))}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	state := eventSnapshot(protocol.PickerCP, protocol.ModeAdd, pathutil.Filesystem([]byte(root))).state
	if _, err := actor.Apply(context.Background(), ProposedTransition{BaseGeneration: 0, State: state}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("one/two")})
	target := filepath.Join(root, "one", "two")
	if err != nil || result.Snapshot.Generation() != 1 || result.Snapshot.State().Mode != protocol.ModeNormal ||
		string(result.Snapshot.State().Location.Path) != target {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("published directory missing: %v", err)
	}
}

func TestHandleInvalidAddRetainsGenerationRecordsAndQuery(t *testing.T) {
	record := eventRecord(protocol.KindFile, "one", "/work/one")
	generated := false
	actor := New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		if generated {
			return candidate.BuildResult{}, errors.New("unexpected generation")
		}
		generated = true
		return candidate.BuildResult{Records: []candidate.Record{record}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	state := eventSnapshot(protocol.PickerCP, protocol.ModeAdd, pathutil.Drives(), record).state
	if _, err := actor.Apply(context.Background(), ProposedTransition{
		BaseGeneration: 0, State: state, Build: &candidate.BuildRequest{Picker: protocol.PickerCP, Location: pathutil.Drives()},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("child")})
	wantEffect := protocol.Effect{Prompt: "[A!] ", ErrorPrompt: true}
	if err != nil || result.Snapshot.Generation() != 1 || len(result.Snapshot.Records()) != 1 ||
		result.Snapshot.State().Mode != protocol.ModeAdd || !result.Snapshot.State().AddError || result.Effect != wantEffect {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
