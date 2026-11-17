package session

import (
	"context"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestHandleForcedFilesSearchFromCDPreservesBrowserAndAcceptsFiles(t *testing.T) {
	ctx := context.Background()
	local := eventRecord(protocol.KindLocal, "local", "/work/local")
	file := eventRecord(protocol.KindFile, "report", "/other/report")
	dir := eventRecord(protocol.KindDirectory, "other", "/other")
	actor := New(ctx, func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if request.Source == protocol.SourceIndexed {
			if request.Picker == protocol.PickerCD {
				return candidate.BuildResult{Records: []candidate.Record{dir}}, nil
			}
			return candidate.BuildResult{Records: []candidate.Record{file, dir}}, nil
		}
		return candidate.BuildResult{Records: []candidate.Record{local}}, nil
	})
	defer actor.Close()
	if _, err := actor.Apply(ctx, ProposedTransition{
		State: State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte(sessionTestPath("/work")))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD},
	}); err != nil {
		t.Fatal(err)
	}
	// First enter CD's directory-only index, then widen it without toggling out.
	if _, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("local")}); err != nil {
		t.Fatal(err)
	}
	result, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.Opcode("fa"), Query: []byte("other")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.RecordCount() != 2 || result.Snapshot.State().Source != protocol.SourceIndexed {
		t.Fatalf("forced files search=%+v", result)
	}
	for _, record := range []candidate.Record{file, dir} {
		result, err = Handle(ctx, actor, protocol.Event{Opcode: protocol.OpMarkToggle, CurrentItem: []byte(record.FullKey())})
		if err != nil {
			t.Fatal(err)
		}
	}
	outcome, err := ValidateSearch(result.Snapshot, nil)
	if err != nil || len(outcome.Paths) != 2 {
		t.Fatalf("forced multi-selection=%+v err=%v", outcome, err)
	}
	back, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("other")})
	if err != nil || back.Snapshot.RecordCount() != 1 || string(back.Snapshot.State().Query) != "local" {
		t.Fatalf("browser restoration=%+v err=%v", back, err)
	}
	again, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.Opcode("fa"), Query: []byte("local")})
	if err != nil || again.Snapshot.RecordCount() != 2 {
		t.Fatalf("force from browser=%+v err=%v", again, err)
	}
	// Repeating the shortcut stays in files search and retains its marks.
	again, err = Handle(ctx, actor, protocol.Event{Opcode: protocol.Opcode("fa"), Query: []byte("report")})
	outcome, selectionErr := ValidateSearch(again.Snapshot, nil)
	if err != nil || selectionErr != nil || len(outcome.Paths) != 2 {
		t.Fatalf("repeated force=%+v err=%v selection=%v", again, err, selectionErr)
	}
}
