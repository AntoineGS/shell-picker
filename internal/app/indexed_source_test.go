package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestIndexedSourceCachesSuccessfulSnapshotAndRetriesFailure(t *testing.T) {
	var calls atomic.Int32
	source := newIndexedSource("/home/test", []string{"XDG_DATA_HOME=/data"}, func(_ context.Context, path string) (*fsearchdb.Database, error) {
		if path != "/data/fsearch/fsearch.db" {
			t.Fatalf("load path = %q", path)
		}
		if calls.Add(1) == 1 {
			return nil, errors.New("busy")
		}
		return &fsearchdb.Database{}, nil
	})
	if _, err := source.load(context.Background()); err == nil {
		t.Fatal("first load succeeded; want retryable failure")
	}
	first, err := source.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || calls.Load() != 2 {
		t.Fatalf("load calls = %d, snapshots %p and %p", calls.Load(), first, second)
	}
}

func TestIndexedFrameSizePreflightRejectsOverLimitCandidates(t *testing.T) {
	display := strings.Repeat("x", 1<<20)
	records := make([]candidate.Record, int(sessionipc.MaxLoadResponseBytes>>20)+1)
	for index := range records {
		records[index] = candidate.Record{
			Kind: protocol.KindFile, Display: display, Payload: "eA==",
		}
	}
	if size := indexedFrameSize(records); size <= uint64(sessionipc.MaxLoadResponseBytes) {
		t.Fatalf("candidate frame size = %d, want greater than %d", size, sessionipc.MaxLoadResponseBytes)
	}
	if size := indexedFrameSize([]candidate.Record{{Kind: protocol.KindFile, Display: "x", Payload: "eA=="}}); size != 12 {
		t.Fatalf("small candidate frame size = %d, want 12", size)
	}
}

func TestInjectedIndexedFrameLimitRejectsBeforeSourceCommit(t *testing.T) {
	ctx := context.Background()
	source := newIndexedSource("/home/test", nil, func(context.Context, string) (*fsearchdb.Database, error) {
		return &fsearchdb.Database{Entries: []fsearchdb.Entry{{Path: []byte("/work/indexed")}}}, nil
	})
	source.maxFrameBytes = 1
	actor := session.New(ctx, func(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if request.Source == protocol.SourceIndexed {
			return source.build(ctx, request)
		}
		return candidate.BuildResult{}, nil
	})
	defer actor.Close()

	location := pathutil.Filesystem([]byte("/work"))
	initialState := session.State{
		Picker: protocol.PickerCP, Source: protocol.SourceBrowser, Mode: protocol.ModeInsert,
		Location: location, Home: pathutil.Filesystem([]byte("/home/test")),
		Browser: &session.BrowserContext{Location: location, Mode: protocol.ModeInsert, SelectedPaths: [][]byte{}},
	}
	initial, err := actor.Apply(ctx, session.ProposedTransition{
		State: initialState,
		Build: &candidate.BuildRequest{Picker: protocol.PickerCP, Source: protocol.SourceBrowser, Location: location},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := session.Handle(ctx, actor, protocol.Event{Opcode: protocol.OpSourceToggle})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Generation() != initial.Snapshot.Generation() ||
		result.Snapshot.State().Source != protocol.SourceBrowser ||
		result.Effect.Header != expectedIndexedSourceLabel()+" unavailable — Ctrl+F to retry" {
		t.Fatalf("oversize transition committed: generation=%d source=%v effect=%+v",
			result.Snapshot.Generation(), result.Snapshot.State().Source, result.Effect)
	}
}

func TestIndexedSourceCanceledLoadCanRetry(t *testing.T) {
	var calls atomic.Int32
	source := newIndexedSource("/home/test", nil, func(ctx context.Context, _ string) (*fsearchdb.Database, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return nil, context.Cause(ctx)
		}
		return &fsearchdb.Database{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load error = %v", err)
	}
	if _, err := source.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("load calls = %d, want 2", calls.Load())
	}
}
