package session

import (
	"context"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSourceToggleRoundTripRetainsZoxideRecordsAndMarks(t *testing.T) {
	ctx := context.Background()
	local := eventRecord(protocol.KindLocal, "local", "/work/local")
	zoxide := eventRecord(protocol.KindZoxide, "elsewhere", "/elsewhere")
	indexed := eventRecord(protocol.KindDirectory, "indexed", "/indexed")
	actor := New(ctx, func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if request.Source == protocol.SourceIndexed {
			return candidate.BuildResult{Records: []candidate.Record{indexed}}, nil
		}
		return candidate.BuildResult{Records: []candidate.Record{local}}, nil
	})
	defer actor.Close()
	initial, err := actor.Apply(ctx, ProposedTransition{
		State: State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte(sessionTestPath("/work")))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actor.Enrich(ctx, initial.Snapshot.Generation(), []candidate.Record{local, zoxide}, candidate.SourceMetrics{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpMarkToggle, CurrentItem: []byte(zoxide.FullKey())}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("elsewhere")}); err != nil {
			t.Fatal(err)
		}
		result, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("indexed")})
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := ValidateCD(result.Snapshot, nil)
		if err != nil || len(outcome.Paths) != 1 || string(outcome.Paths[0]) != sessionTestPath("/elsewhere") {
			t.Fatalf("restored zoxide selection=%+v err=%v; records=%+v", outcome, err, result.Snapshot.Records())
		}
		if string(result.Snapshot.State().Query) != "elsewhere" || result.Effect.RestorePositions != "2" {
			t.Fatalf("restoration=%+v", result)
		}
	}
}
