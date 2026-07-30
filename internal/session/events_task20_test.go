package session

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestHandleAddPublishesCompleteStateAtomicallyAfterSingleGeneration(t *testing.T) {
	base := t.TempDir()
	started, release := make(chan struct{}), make(chan struct{})
	var generations atomic.Int32
	createdRecord := eventRecord(protocol.KindDirectory, "created", filepath.Join(base, "new", "child"))
	actor := New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		if generations.Add(1) != 1 {
			t.Error("Add started more than one candidate generation")
		}
		close(started)
		<-release
		return candidate.BuildResult{Records: []candidate.Record{createdRecord}}, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = actor.Close()
	})
	state := eventSnapshot(protocol.PickerCD, protocol.ModeAdd, pathutil.Filesystem([]byte(base))).state
	initial, err := actor.Apply(context.Background(), ProposedTransition{State: state})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := asyncHandle(actor, ctx, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new/child")})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("standalone Handle did not start candidate generation")
	}
	target := filepath.Join(base, "new", "child")
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("standalone Handle did not create the requested tree: %v", err)
	}
	old, err := actor.Current(ctx)
	if err != nil || old.Generation() != initial.Snapshot.Generation() || old.State().Mode != protocol.ModeAdd ||
		string(old.State().Location.Path) != base || len(old.Records()) != 0 {
		t.Fatalf("pending Add changed old snapshot: generation=%d state=%+v records=%d err=%v",
			old.Generation(), old.State(), len(old.Records()), err)
	}
	close(release)
	outcome := awaitApply(t, done)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	newSnapshot := outcome.result.Snapshot
	if generations.Load() != 1 || newSnapshot.Generation() != initial.Snapshot.Generation()+1 ||
		newSnapshot.State().Mode != protocol.ModeNormal || string(newSnapshot.State().Location.Path) != target ||
		len(newSnapshot.Records()) != 1 || newSnapshot.Records()[0].FullKey() != createdRecord.FullKey() ||
		outcome.result.Effect.ReloadGeneration != newSnapshot.Generation() {
		t.Fatalf("Add publication was incomplete: generations=%d snapshot=%+v effect=%+v",
			generations.Load(), newSnapshot.State(), outcome.result.Effect)
	}
	if _, err := actor.ResolveCurrent(ctx, createdRecord.Wire().Bytes()); err != nil {
		t.Fatalf("published Add index is incomplete: %v", err)
	}
}
