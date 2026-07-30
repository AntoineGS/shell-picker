package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestForgedPayloadCannotAuthorizePreviewOrSelection(t *testing.T) {
	allowed := candidate.Record{
		Kind: protocol.KindDirectory, Display: "allowed", Path: []byte("/allowed"),
		Payload: protocol.EncodePath([]byte("/allowed")), Target: pathutil.Filesystem([]byte("/allowed")),
	}
	var builds atomic.Int32
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		builds.Add(1)
		return candidate.BuildResult{Records: []candidate.Record{allowed}}, nil
	})
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close actor: %v", err)
		}
	})
	result, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	known := result.Snapshot.Records()[0].Wire()
	forged := protocol.WireRecord{Kind: known.Kind, Display: "forged", Payload: known.Payload}.Bytes()
	if string(forged) == string(known.Bytes()) {
		t.Fatal("forged record unexpectedly equals authorized record")
	}
	if _, err := actor.ResolveCurrent(context.Background(), forged); !errors.Is(err, session.ErrUnknownRecord) {
		t.Fatalf("preview resolution error = %v; want ErrUnknownRecord", err)
	}
	if _, err := session.ValidateCD(result.Snapshot, [][]byte{forged}); !errors.Is(err, session.ErrUnknownSelection) {
		t.Fatalf("selection error = %v; want ErrUnknownSelection", err)
	}
	if builds.Load() != 1 {
		t.Fatalf("forged identity reached candidate generation: builds=%d", builds.Load())
	}
}

func TestCancelledNavigationAndPreviewLeakNothing(t *testing.T) {
	ownedArtifacts := t.TempDir()
	baseline := snapshotResources(t, ownedArtifacts)
	for range 10 {
		runCancelledPreviewHandler(t)
		started := make(chan struct{})
		exited := make(chan struct{})
		var onceStart, onceExit sync.Once
		runner := process.Runner{Observe: func(event process.ProcessEvent) {
			switch event.Phase {
			case "start":
				onceStart.Do(func() { close(started) })
			case "exit":
				onceExit.Do(func() { close(exited) })
			}
		}}
		ctx, cancel := context.WithCancelCause(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- runner.Run(ctx, process.Spec{Path: os.Args[0], Args: []string{"-test.run=^TestTask20BlockingHelper$"},
				Env: append(os.Environ(), "SHELL_PICKER_TASK20_BLOCK=1"), Containment: process.ContainmentOwnTree,
				WaitDelay: time.Second})
		}()
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("child returned before start event: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("child did not reach start event")
		}
		cause := errors.New("cancel adversarial preview")
		cancel(cause)
		select {
		case err := <-done:
			if !errors.Is(err, cause) {
				t.Fatalf("cancelled process error = %v; want exact cause", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled child did not return")
		}
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled child emitted no exit event")
		}
	}
	assertResourcesReturned(t, baseline, ownedArtifacts)
}

type blockingPreviewBackend struct {
	started chan struct{}
	once    sync.Once
}

func (backend *blockingPreviewBackend) HandleEvent(context.Context, protocol.Event) (protocol.Effect, error) {
	return protocol.Effect{}, nil
}

func (backend *blockingPreviewBackend) LoadGeneration(context.Context, uint64) ([]byte, error) {
	return nil, nil
}

func (backend *blockingPreviewBackend) ResolvePreview(ctx context.Context, _ []byte) (protocol.ResolvedCandidate, error) {
	backend.once.Do(func() { close(backend.started) })
	<-ctx.Done()
	return protocol.ResolvedCandidate{}, context.Cause(ctx)
}

func (backend *blockingPreviewBackend) RecordPreview(context.Context, sessionipc.PreviewRequest) error {
	return nil
}

func runCancelledPreviewHandler(t *testing.T) {
	t.Helper()
	token, err := sessionipc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	backend := &blockingPreviewBackend{started: make(chan struct{})}
	server, err := sessionipc.Listen(context.Background(), token, backend)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sessionipc.NewClientFromEnv(func(key string) string {
		switch key {
		case "SHELL_PICKER_ADDR":
			return server.Address()
		case "SHELL_PICKER_TOKEN":
			return token.String()
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	requestDone := make(chan error, 1)
	go func() {
		_, requestErr := client.ResolvePreview(context.Background(), sessionipc.PreviewRequest{
			Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("current-record")),
		})
		requestDone <- requestErr
	}()
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-backend.started:
	case err := <-requestDone:
		t.Fatalf("preview request returned before backend entry: %v", err)
	case <-closeCtx.Done():
		t.Fatal("preview request did not reach backend")
	}
	if err := server.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("cancelled preview request unexpectedly succeeded")
		}
	case <-closeCtx.Done():
		t.Fatal("preview handler did not join after listener shutdown")
	}
	client.CloseIdleConnections()
}

func TestTask20BlockingHelper(t *testing.T) {
	if os.Getenv("SHELL_PICKER_TASK20_BLOCK") != "1" {
		return
	}
	select {}
}
