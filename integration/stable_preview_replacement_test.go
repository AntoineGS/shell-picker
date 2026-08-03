//go:build linux || windows

package integration

import (
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

var stablePreviewReplacementMutation atomic.Uint64

type stablePreviewBackend struct {
	path string
	mu   sync.Mutex
	seen []sessionipc.PreviewRequest
}

func (backend *stablePreviewBackend) HandleEvent(context.Context, protocol.Event) (sessionipc.EventResult, error) {
	return sessionipc.EventResult{}, nil
}

func (backend *stablePreviewBackend) LoadGeneration(context.Context, sessionipc.LoadRequest) ([]byte, error) {
	return nil, nil
}

func (backend *stablePreviewBackend) CurrentHeader(context.Context) (string, error) {
	return "", nil
}

func (backend *stablePreviewBackend) ResolvePreview(context.Context, []byte) (protocol.ResolvedCandidate, error) {
	info, err := os.Stat(backend.path)
	if err != nil {
		return protocol.ResolvedCandidate{}, err
	}
	return protocol.ResolvedCandidate{Kind: protocol.KindDirectory, Path: []byte(backend.path), Size: info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}, nil
}

func (backend *stablePreviewBackend) RecordPreview(_ context.Context, request sessionipc.PreviewRequest) error {
	backend.mu.Lock()
	backend.seen = append(backend.seen, request)
	backend.mu.Unlock()
	return nil
}

func (backend *stablePreviewBackend) finished() []sessionipc.PreviewRequest {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	result := make([]sessionipc.PreviewRequest, 0, len(backend.seen))
	for _, request := range backend.seen {
		if request.Phase == "finished" {
			result = append(result, request)
		}
	}
	return result
}

type stablePreviewBudgets struct {
	steadyTrees, replacementTrees int
	oldExit                       time.Duration
	maxLiveChildren               int
	sequentialChildren            int
	nativeChildren                int
}

func assertStablePreviewReplacementBudgets(t *testing.T, measured stablePreviewBudgets) {
	t.Helper()
	stablePreviewReplacementMutation.Add(1)
	if measured.steadyTrees != 1 || measured.replacementTrees > 2 {
		t.Fatalf("preview trees steady/replacement=%d/%d limits=1/2", measured.steadyTrees, measured.replacementTrees)
	}
	if measured.oldExit > 3*time.Second {
		t.Fatalf("old preview tree exit=%s limit=3s", measured.oldExit)
	}
	if measured.maxLiveChildren > 1 || measured.sequentialChildren > 3 || measured.nativeChildren != 0 {
		t.Fatalf("preview child budgets max-live/sequential/native=%d/%d/%d limits=1/3/0",
			measured.maxLiveChildren, measured.sequentialChildren, measured.nativeChildren)
	}
}

func TestStablePreviewReplacementBudgets(t *testing.T) {
	fixture := newBlockingPreviewFixture(t, "")
	backend := &stablePreviewBackend{path: fixture.cwd}
	token, err := sessionipc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	server, err := sessionipc.Listen(context.Background(), token, backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	path := fixture.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	environment := replaceEnvironment(os.Environ(), "PATH="+path, "SHELL_PICKER_ADDR="+server.Address(),
		"SHELL_PICKER_TOKEN="+token.String(), "FZF_CURRENT_ITEM=stable", "FZF_PREVIEW_COLUMNS=80", "FZF_PREVIEW_LINES=24")
	var starts atomic.Int32
	startCallback := func(environment []string) *process.Child {
		child, startErr := (process.Runner{Observe: func(event process.ProcessEvent) {
			if event.Phase == "start" {
				starts.Add(1)
			}
		}}).Start(context.Background(), process.Spec{Path: fixture.picker, Args: []string{"--fzf-shell", "p"},
			Env: environment, Stdout: io.Discard, Stderr: io.Discard, Containment: process.ContainmentOwnTree, WaitDelay: time.Second})
		if startErr != nil {
			t.Fatal(startErr)
		}
		return child
	}

	firstChild := startCallback(environment)
	first := fixture.waitTree(t, 1)
	defer first.close()
	steadyTrees := int(starts.Load())
	startedExit := time.Now()
	if err := firstChild.KillTree(); err != nil {
		t.Fatal(err)
	}
	_ = firstChild.Wait()
	exitCtx, cancelExit := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelExit()
	if err := waitTreeExit(exitCtx, first); err != nil {
		t.Fatalf("old callback/renderer tree remained: %v", err)
	}
	oldExit := time.Since(startedExit)

	secondChild := startCallback(environment)
	second := fixture.waitTree(t, 2)
	defer second.close()
	replacementTrees := int(starts.Load())
	if first.CallbackPID == second.CallbackPID || first.RendererPID == second.RendererPID || first.GrandchildPID == second.GrandchildPID {
		t.Fatalf("replacement reused OS process identity: first=%+v second=%+v", first, second)
	}
	if err := fixture.controller.release(second.RendererPID); err != nil {
		t.Fatal(err)
	}
	if err := secondChild.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := waitTreeExit(testContext(t), second); err != nil {
		t.Fatalf("replacement callback/renderer tree remained: %v", err)
	}
	finished := backend.finished()
	if len(finished) != 1 {
		t.Fatalf("finished preview telemetry=%+v want one replacement completion", finished)
	}

	nativeEnvironment := replaceEnvironment(environment, "PATH="+t.TempDir())
	nativeChild := startCallback(nativeEnvironment)
	if err := nativeChild.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = backend.finished()
	if len(finished) != 2 || finished[1].Renderer != "native" {
		t.Fatalf("native preview telemetry=%+v", finished)
	}
	assertStablePreviewReplacementBudgets(t, stablePreviewBudgets{
		steadyTrees: steadyTrees, replacementTrees: replacementTrees, oldExit: oldExit,
		maxLiveChildren: finished[0].MaxLiveChildren, sequentialChildren: finished[0].ChildStarts,
		nativeChildren: finished[1].ChildStarts,
	})
}

func TestStablePreviewReplacementRunsWithoutRealFZFEnvironment(t *testing.T) {
	t.Setenv("SHELL_PICKER_REAL_FZF", "")
	before := stablePreviewReplacementMutation.Load()
	assertStablePreviewReplacementBudgets(t, stablePreviewBudgets{steadyTrees: 1})
	if stablePreviewReplacementMutation.Load() != before+1 {
		t.Fatal("stable preview replacement assertion did not execute")
	}
}
