package callback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestEventRendersReloadWithExactLoadEventID(t *testing.T) {
	client := &fakeClient{effect: protocol.Effect{ReloadGeneration: 7}, eventID: 9}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(key string) string {
		if key == "FZF_KEY" {
			return "left"
		}
		return ""
	}, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "e:up"), deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "reload-sync(l:7:9)") {
		t.Fatalf("stdout=%q, want exact load event ID", stdout.String())
	}
}

func TestLoadFinalizesExactEventAfterAllBytesAreWritten(t *testing.T) {
	client := &fakeClient{load: []byte("load-data")}
	var stdout bytes.Buffer
	client.onLoadFinalize = func(_ context.Context, request sessionipc.LoadFinalizeRequest) {
		if stdout.String() != "load-data" {
			t.Fatalf("load finalized before bytes were written: %q", stdout.String())
		}
		if request.EventID != 9 || !request.Applied {
			t.Fatalf("load finalization=%+v", request)
		}
	}
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "l:42:9"), deps); err != nil {
		t.Fatal(err)
	}
	if len(client.loads) != 1 || client.loads[0] != (sessionipc.LoadRequest{Generation: 42, EventID: 9}) || client.loadFinalizeCount != 1 {
		t.Fatalf("loads=%+v finalizations=%d", client.loads, client.loadFinalizeCount)
	}
}

func TestLoadFinalizesUnappliedAfterWriteFailure(t *testing.T) {
	client := &fakeClient{load: []byte("load-data")}
	var request sessionipc.LoadFinalizeRequest
	client.onLoadFinalize = func(_ context.Context, got sessionipc.LoadFinalizeRequest) { request = got }
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: &shortWriter{maximum: 0}, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "l:42:9"), deps); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Dispatch error=%v, want short write", err)
	}
	if request.EventID != 9 || request.Applied {
		t.Fatalf("load finalization=%+v, want exact unapplied request", request)
	}
}

func TestLoadFinalizesUnappliedAfterRequestFailure(t *testing.T) {
	requestErr := errors.New("load request failed")
	client := &fakeClient{err: requestErr}
	var finalized sessionipc.LoadFinalizeRequest
	client.onLoadFinalize = func(_ context.Context, request sessionipc.LoadFinalizeRequest) { finalized = request }
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: io.Discard, Stderr: io.Discard}
	if err := Dispatch(context.Background(), Command{Kind: KindLoad, Generation: 7, EventID: 9}, deps); !errors.Is(err, requestErr) {
		t.Fatalf("Dispatch error=%v, want request error", err)
	}
	if client.loadFinalizeCount != 1 || finalized != (sessionipc.LoadFinalizeRequest{EventID: 9, Applied: false}) {
		t.Fatalf("finalize count=%d request=%+v, want one exact unapplied finalization", client.loadFinalizeCount, finalized)
	}
}

func TestLoadFinalizesUnappliedWithDetachedContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{err: context.Canceled}
	var finalizerContext context.Context
	var finalized sessionipc.LoadFinalizeRequest
	client.onLoadFinalize = func(ctx context.Context, request sessionipc.LoadFinalizeRequest) {
		finalizerContext = ctx
		finalized = request
	}
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: io.Discard, Stderr: io.Discard}
	if err := Dispatch(ctx, Command{Kind: KindLoad, Generation: 7, EventID: 9}, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("Dispatch error=%v, want cancellation", err)
	}
	if finalizerContext == nil || finalizerContext.Err() != nil {
		t.Fatalf("finalizer context=%v, want detached usable context", finalizerContext)
	}
	if client.loadFinalizeCount != 1 || finalized != (sessionipc.LoadFinalizeRequest{EventID: 9, Applied: false}) {
		t.Fatalf("finalize count=%d request=%+v, want one exact unapplied finalization", client.loadFinalizeCount, finalized)
	}
}

func TestLoadRequestIncludesEventIDForCoordinatorGrammar(t *testing.T) {
	client := &fakeClient{load: []byte{'a', 0, '\n', 0xff}}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "l:42:9"), deps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.loads, []sessionipc.LoadRequest{{Generation: 42, EventID: 9}}) {
		t.Fatalf("loads=%+v", client.loads)
	}
}

func TestLoadWritesExactOctets(t *testing.T) {
	client := &fakeClient{load: []byte{'a', 0, '\n', 0xff}}
	var stdout bytes.Buffer
	deps := Dependencies{Client: client, LookupEnv: func(string) string { return "" }, Stdout: &stdout, Stderr: io.Discard}
	if err := Dispatch(context.Background(), mustParse(t, "l:42"), deps); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), client.load) || !reflect.DeepEqual(client.loads, []sessionipc.LoadRequest{{Generation: 42}}) {
		t.Fatalf("output=%v loads=%+v", stdout.Bytes(), client.loads)
	}
}
