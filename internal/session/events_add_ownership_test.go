package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type cancelWhenPathExistsContext struct {
	context.Context
	path string
}

func (ctx cancelWhenPathExistsContext) Err() error {
	if _, err := os.Lstat(ctx.path); err == nil {
		return context.Canceled
	}
	return nil
}

func readyAddActor(t *testing.T, sessionCtx context.Context, base string, generate GenerateFunc) *Actor {
	t.Helper()
	actor := New(sessionCtx, generate)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	state := eventSnapshot(protocol.PickerCD, protocol.ModeAdd, pathutil.Filesystem([]byte(base))).state
	if _, err := actor.Apply(context.Background(), ProposedTransition{BaseGeneration: 0, State: state}); err != nil {
		t.Fatal(err)
	}
	return actor
}

func asyncHandle(actor *Actor, ctx context.Context, event protocol.Event) <-chan applyOutcome {
	done := make(chan applyOutcome, 1)
	go func() {
		result, err := Handle(ctx, actor, event)
		done <- applyOutcome{result: result, err: err}
	}()
	return done
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("path %q does not exist as directory: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("path %q remains: %v", path, err)
	}
}

func TestHandleAddCreateErrorsRetainAddSnapshotAndDoNotBuild(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "taken"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := eventRecord(protocol.KindFile, "record", filepath.Join(base, "record"))
	var builds atomic.Int32
	actor := New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		builds.Add(1)
		return candidate.BuildResult{Records: []candidate.Record{record}}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	state := eventSnapshot(protocol.PickerCD, protocol.ModeAdd, pathutil.Filesystem([]byte(base))).state
	initial, err := actor.Apply(context.Background(), ProposedTransition{
		BaseGeneration: 0, State: state,
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: state.Location},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		query string
	}{
		{"existing file", "taken/child"},
		{"partial create", "temporary/" + strings.Repeat("x", 256)},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, handleErr := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte(test.query)})
			wantEffect := protocol.Effect{Prompt: modePrompt(protocol.ModeAdd, state.Location, true), ErrorPrompt: true}
			if handleErr != nil || result.Effect != wantEffect || result.Snapshot.Generation() != initial.Snapshot.Generation() ||
				result.Snapshot.State().Mode != protocol.ModeAdd || !result.Snapshot.State().AddError || len(result.Snapshot.Records()) != 1 {
				t.Fatalf("result=%+v err=%v", result, handleErr)
			}
		})
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build count=%d; want initial build only", got)
	}
	assertPathMissing(t, filepath.Join(base, "temporary"))
	if contents, err := os.ReadFile(filepath.Join(base, "taken")); err != nil || string(contents) != "x" {
		t.Fatalf("taken file=%q err=%v", contents, err)
	}
}

func TestHandleAddGenerationFailureRollsBackAndPreservesParents(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "existing")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	generator := newControlledGenerator()
	actor := readyAddActor(t, context.Background(), base, generator.Generate)
	done := asyncHandle(actor, context.Background(), protocol.Event{Opcode: protocol.OpEnter, Query: []byte("existing/new/child")})
	call := generator.Next(t)
	t.Cleanup(func() { call.Complete(nil, context.Canceled) })
	target := filepath.Join(parent, "new", "child")
	assertPathExists(t, target)
	call.Complete(nil, fs.ErrPermission)
	if outcome := awaitApply(t, done); !errors.Is(outcome.err, fs.ErrPermission) {
		t.Fatalf("Handle() = %v", outcome.err)
	}
	assertPathMissing(t, filepath.Join(parent, "new"))
	assertPathExists(t, parent)
}

func TestHandleAddCancellationBeforeApplyUsesLocalRollback(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "new", "child")
	var builds atomic.Int32
	actor := readyAddActor(t, context.Background(), base, func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		builds.Add(1)
		return candidate.BuildResult{}, nil
	})
	ctx := cancelWhenPathExistsContext{Context: context.Background(), path: target}
	result, err := Handle(ctx, actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new/child")})
	if !errors.Is(err, context.Canceled) || result.Snapshot.Generation() != 0 || result.Effect != (protocol.Effect{}) || builds.Load() != 0 {
		t.Fatalf("result=%+v err=%v builds=%d", result, err, builds.Load())
	}
	assertPathMissing(t, filepath.Join(base, "new"))
	snapshot, currentErr := actor.Current(context.Background())
	if currentErr != nil || snapshot.Generation() != 0 || snapshot.State().Mode != protocol.ModeAdd {
		t.Fatalf("snapshot=%+v err=%v", snapshot, currentErr)
	}
}

func TestHandleAddRollbackWaitsForGenerationCompletion(t *testing.T) {
	for _, kind := range []string{"caller", "session", "close", "supersede"} {
		t.Run(kind, func(t *testing.T) {
			sessionCtx, stopSession := context.WithCancelCause(context.Background())
			t.Cleanup(func() { stopSession(context.Canceled) })
			generator := newControlledGenerator()
			base := t.TempDir()
			actor := readyAddActor(t, sessionCtx, base, generator.Generate)
			callerCtx, stopCaller := context.WithCancel(context.Background())
			t.Cleanup(stopCaller)
			done := asyncHandle(actor, callerCtx, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new/child")})
			call := generator.Next(t)
			t.Cleanup(func() { call.Complete(nil, context.Canceled) })
			target := filepath.Join(base, "new", "child")

			var replacement <-chan applyOutcome
			var closed <-chan error
			switch kind {
			case "caller":
				stopCaller()
			case "session":
				stopSession(context.Canceled)
			case "close":
				closeResult := make(chan error, 1)
				go func() { closeResult <- actor.Close() }()
				closed = closeResult
			case "supersede":
				replacement = asyncApply(actor, context.Background(), ProposedTransition{
					BaseGeneration: 0,
					State:          testState("/replacement", protocol.ModeNormal, "replacement"),
				})
			}
			select {
			case <-call.ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("generation was not cancelled")
			}
			assertPathExists(t, target)
			call.Complete(nil, context.Canceled)
			outcome := awaitApply(t, done)
			if outcome.err == nil {
				t.Fatal("Handle unexpectedly succeeded")
			}
			assertPathMissing(t, filepath.Join(base, "new"))
			if replacement != nil {
				if replacementOutcome := awaitApply(t, replacement); replacementOutcome.err != nil {
					t.Fatalf("replacement = %v", replacementOutcome.err)
				}
			}
			if closed != nil {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("Close did not return")
				}
			}
		})
	}
}

func TestActorOwnsStaleAddTreeRollback(t *testing.T) {
	base := t.TempDir()
	actor := New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	state := eventSnapshot(protocol.PickerCD, protocol.ModeAdd, pathutil.Filesystem([]byte(base))).state
	if _, err := actor.Apply(context.Background(), ProposedTransition{
		BaseGeneration: 0, State: state,
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: state.Location},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := pathutil.CreateDirectoryTree(state.Location, []byte("stale/child"))
	if err != nil {
		t.Fatal(err)
	}
	proposal := ProposedTransition{BaseGeneration: 0, State: state, Created: &created}
	setNavigationUnchecked(&proposal, created.Target)
	if _, err := actor.Apply(context.Background(), proposal); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("Apply() = %v", err)
	}
	assertPathMissing(t, filepath.Join(base, "stale"))
}

func TestHandleAddRejectsSymlinkTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse coverage is in pathutil Windows tests")
	}
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	actor := readyAddActor(t, context.Background(), base, func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		builds.Add(1)
		return candidate.BuildResult{}, nil
	})
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("link/child")})
	if err != nil || !result.Effect.ErrorPrompt || result.Snapshot.State().Mode != protocol.ModeAdd || builds.Load() != 0 {
		t.Fatalf("result=%+v err=%v builds=%d", result, err, builds.Load())
	}
	assertPathMissing(t, filepath.Join(outside, "child"))
}
