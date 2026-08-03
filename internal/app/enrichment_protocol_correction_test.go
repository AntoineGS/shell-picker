package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/callback"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type failingLoadBackend struct {
	sessionipc.Backend
	err error
}

func (backend failingLoadBackend) LoadGeneration(context.Context, sessionipc.LoadRequest) ([]byte, error) {
	return nil, backend.err
}

func (backend failingLoadBackend) FinalizeEvent(ctx context.Context, request sessionipc.EventFinalizeRequest) error {
	return backend.Backend.(sessionipc.EventFinalizer).FinalizeEvent(ctx, request)
}

func (backend failingLoadBackend) FinalizeLoad(ctx context.Context, request sessionipc.LoadFinalizeRequest) error {
	return backend.Backend.(sessionipc.LoadFinalizer).FinalizeLoad(ctx, request)
}

func TestInitialEnrichmentActiveModeChangePublishesLateSourceWithoutRestoreLoad(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/late-mode"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	result, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpModeAdd})
	if err != nil {
		t.Fatalf("mode event: %v", err)
	}
	if result.Effect.RestoreGeneration != 0 || result.Effect.Mode != protocol.ModeAdd || result.Effect.ClearQuery != true {
		t.Fatalf("active mode result=%+v, want mode effects without restore load", result)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	data, err := readEnrichmentStream(t, stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !bytes.Contains(data, []byte("late-mode")) {
		t.Fatalf("stream=%q, want late source appended", data)
	}
	current := currentEnrichmentSnapshot(t, actor)
	if current.State().Mode != protocol.ModeAdd || current.Generation() != 2 {
		t.Fatalf("snapshot=%+v, want mode add with enrichment generation", current)
	}
}

func TestInitialEnrichmentRestoreDiscardsActiveSourceAndRestoresExactGeneration(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		canceled: make(chan struct{}), result: enrichmentSource("/late-restore"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	invalid, err := fzf.RenderEffect(protocol.Effect{Put: "/", InvalidPath: true})
	if err != nil || invalid != "put(/)+reload-sync(l:empty)+change-preview(p:invalid)+rebind(result-final)" {
		t.Fatalf("invalid transient action=%q err=%v", invalid, err)
	}
	oldGeneration := currentEnrichmentSnapshot(t, actor).Generation()
	restore, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView})
	if err != nil {
		t.Fatalf("restore event: %v", err)
	}
	if restore.EventID == 0 || restore.Effect.RestoreGeneration != oldGeneration {
		t.Fatalf("restore=%+v, want exact generation=%d and event ID", restore, oldGeneration)
	}
	action, err := fzf.RenderEffectForEvent(restore.Effect, restore.EventID)
	if err != nil {
		t.Fatalf("render restore: %v", err)
	}
	if !strings.Contains(action, fmt.Sprintf("reload-sync(l:%d:%d)", oldGeneration, restore.EventID)) || !strings.Contains(action, "change-preview(p)") ||
		!strings.Contains(action, "unbind(change,result-final)") {
		t.Fatalf("restore action=%q, want exact reload and transient reset", action)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: restore.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	backend := &pickerBackend{actor: actor, enrichment: enrichment, metrics: &pickerMetrics{}}
	data, err := backend.LoadGeneration(context.Background(), sessionipc.LoadRequest{Generation: oldGeneration, EventID: restore.EventID})
	if err != nil {
		t.Fatalf("restore LoadGeneration: %v", err)
	}
	awaitEnrichmentChannel(t, source.canceled, "zoxide cancellation")
	awaitEnrichmentChannel(t, source.finished, "zoxide reap")
	streamDone := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		streamData, streamErr := io.ReadAll(stream)
		streamDone <- struct {
			data []byte
			err  error
		}{streamData, streamErr}
	}()
	select {
	case got := <-streamDone:
		t.Fatalf("input closed before exact load finalization: data=%q err=%v", got.data, got.err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: restore.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeLoad: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("restore discard wait: %v", err)
	}
	var streamData []byte
	select {
	case result := <-streamDone:
		if result.err != nil {
			t.Fatalf("read restored stream: %v", result.err)
		}
		streamData = result.data
	case <-time.After(2 * time.Second):
		t.Fatal("input remained open after exact load finalization")
	}
	if bytes.Contains(streamData, []byte("late-restore")) {
		t.Fatalf("late zoxide append after restore discard: %q", streamData)
	}
	if len(data) == 0 || !bytes.Contains(data, []byte("/base")) {
		t.Fatalf("restore data=%q, want old generation records", data)
	}
}

func TestInitialEnrichmentRestoreIsNotSuppressedAndRequiresExactLoad(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/restore"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	restore, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restore.EventID == 0 || restore.Effect.RestoreGeneration == 0 {
		t.Fatalf("restore=%+v, want exact reload reservation", restore)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: restore.EventID, Applied: true}); err != nil {
		t.Fatalf("early FinalizeEvent: %v", err)
	}
	backend := &pickerBackend{actor: actor, enrichment: enrichment, metrics: &pickerMetrics{}}
	if _, err := backend.LoadGeneration(context.Background(), sessionipc.LoadRequest{Generation: restore.Effect.RestoreGeneration, EventID: restore.EventID}); err != nil {
		t.Fatalf("restore LoadGeneration: %v", err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: restore.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeLoad: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestInitialEnrichmentSerializesEventsUntilExactActionFinalization(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/serialized"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	first, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpModeAdd})
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	secondDone := make(chan error, 1)
	go func() {
		_, secondErr := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView})
		secondDone <- secondErr
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second event returned before first finalization: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: first.EventID, Applied: true}); err != nil {
		t.Fatalf("first FinalizeEvent: %v", err)
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second event: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second event remained blocked after first finalization")
	}
	close(source.release)
}

func TestInitialEnrichmentSerializesSecondEventUntilFirstLoadFinalization(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		ignoreCtx: true, result: enrichmentSource("/serialized-load"),
	}
	enrichment := newTestEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil))
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	first, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: first.EventID, Applied: true}); err != nil {
		t.Fatalf("first FinalizeEvent: %v", err)
	}
	secondDone := make(chan sessionipc.EventResult, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, eventErr := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView})
		secondDone <- result
		secondErr <- eventErr
	}()
	select {
	case <-secondDone:
		t.Fatal("second event returned before first load finalization")
	case <-time.After(25 * time.Millisecond):
	}
	backend := &pickerBackend{actor: actor, enrichment: enrichment, metrics: &pickerMetrics{}}
	request := sessionipc.LoadRequest{Generation: first.Effect.ReloadGeneration, EventID: first.EventID}
	if _, err := backend.LoadGeneration(context.Background(), request); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := enrichment.FinalizeLoad(context.Background(), sessionipc.LoadFinalizeRequest{EventID: first.EventID, Applied: true}); err != nil {
		t.Fatalf("first FinalizeLoad: %v", err)
	}
	select {
	case result := <-secondDone:
		if err := <-secondErr; err != nil || result.Effect.RestoreGeneration == 0 {
			t.Fatalf("second result=%+v err=%v", result, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second event remained blocked after first load finalization")
	}
	close(source.release)
}

func TestInitialEnrichmentAppliedFalseIsHardCallbackFailure(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeNormal)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/failed-callback"),
	}
	stream := fzf.NewInputStream(nil)
	enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	result, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpModeAdd})
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: false}); err == nil {
		t.Fatal("Applied=false finalization returned nil")
	}
	readDone := make(chan error, 1)
	go func() {
		var buffer [1]byte
		_, readErr := stream.Read(buffer[:])
		readDone <- readErr
	}()
	select {
	case readErr := <-readDone:
		if readErr == nil || !strings.Contains(readErr.Error(), "callback application failed") {
			t.Fatalf("input close error=%v", readErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input remained open after Applied=false")
	}
	if err := awaitEnrichmentWait(t, enrichment); err == nil {
		t.Fatal("Wait returned nil after callback application failure")
	} else if !strings.Contains(err.Error(), "callback application failed") {
		t.Fatalf("Wait error=%v, want callback application failure", err)
	}
	if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView}); err == nil || !strings.Contains(err.Error(), "callback application failed") {
		t.Fatalf("event after callback failure=%v", err)
	}
}

func TestRunPickerReturnsHardErrorWhenEventActionWriteFails(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		client := callbackClient(t, config)
		var output zeroWriter
		err := callback.Dispatch(ctx, callback.Command{Kind: callback.KindEvent, Opcode: protocol.OpParent}, callback.Dependencies{
			Client: client,
			LookupEnv: func(key string) string {
				if key == "FZF_KEY" {
					return "left"
				}
				return ""
			},
			Stdout: &output, Stderr: io.Discard,
		})
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("callback error=%v, want short write", err)
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err == nil || !strings.Contains(err.Error(), "callback application failed") {
		t.Fatalf("RunPicker error=%v, want hard callback application failure", err)
	}
}

func TestRunPickerReturnsHardErrorWhenLoadBytesWriteFails(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		client := callbackClient(t, config)
		response, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
		if err != nil {
			t.Fatal(err)
		}
		finalizeTestEvent(t, ctx, client, response, true)
		err = callback.Dispatch(ctx, callback.Command{Kind: callback.KindLoad, Generation: response.Effect.ReloadGeneration, EventID: response.EventID}, callback.Dependencies{
			Client: client, LookupEnv: func(string) string { return "" }, Stdout: zeroWriter{}, Stderr: io.Discard,
		})
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("load callback error=%v, want short write", err)
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err == nil || !strings.Contains(err.Error(), "callback application failed") {
		t.Fatalf("RunPicker error=%v, want hard callback application failure", err)
	}
}

func TestRunPickerReturnsHardErrorWhenLoadRequestFailsBeforeBegin(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	loadErr := errors.New("load request failed before begin")
	fixture.dependencies.listenIPC = func(ctx context.Context, token sessionipc.Token, backend sessionipc.Backend) (*sessionipc.Server, error) {
		return sessionipc.Listen(ctx, token, failingLoadBackend{Backend: backend, err: loadErr})
	}
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		client := callbackClient(t, config)
		response, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
		if err != nil {
			t.Fatal(err)
		}
		finalizeTestEvent(t, ctx, client, response, true)
		err = callback.Dispatch(ctx, callback.Command{Kind: callback.KindLoad, Generation: response.Effect.ReloadGeneration, EventID: response.EventID}, callback.Dependencies{
			Client: client, LookupEnv: func(string) string { return "" }, Stdout: io.Discard, Stderr: io.Discard,
		})
		if err == nil {
			t.Fatal("load callback unexpectedly succeeded")
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err == nil || !strings.Contains(err.Error(), "callback application failed") {
		t.Fatalf("RunPicker error=%v, want hard callback application failure", err)
	}
}
