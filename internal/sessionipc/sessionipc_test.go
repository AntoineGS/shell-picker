package sessionipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

type backendStub struct {
	handleEvent    func(context.Context, protocol.Event) (EventResult, error)
	finalizeEvent  func(EventFinalizeRequest)
	finalizeLoad   func(LoadFinalizeRequest)
	loadGeneration func(context.Context, LoadRequest) ([]byte, error)
	resolvePreview func(context.Context, []byte) (protocol.ResolvedCandidate, error)
	recordPreview  func(context.Context, PreviewRequest) error
	currentHeader  func(context.Context) (string, error)
}

func (b backendStub) HandleEvent(ctx context.Context, event protocol.Event) (EventResult, error) {
	return b.handleEvent(ctx, event)
}

func (b backendStub) FinalizeEvent(_ context.Context, request EventFinalizeRequest) error {
	if b.finalizeEvent != nil {
		b.finalizeEvent(request)
	}
	return nil
}

func (b backendStub) FinalizeLoad(_ context.Context, request LoadFinalizeRequest) error {
	if b.finalizeLoad != nil {
		b.finalizeLoad(request)
	}
	return nil
}

func (b backendStub) LoadGeneration(ctx context.Context, request LoadRequest) ([]byte, error) {
	return b.loadGeneration(ctx, request)
}

func (b backendStub) ResolvePreview(ctx context.Context, current []byte) (protocol.ResolvedCandidate, error) {
	return b.resolvePreview(ctx, current)
}

func (b backendStub) RecordPreview(ctx context.Context, request PreviewRequest) error {
	return b.recordPreview(ctx, request)
}

func (b backendStub) CurrentHeader(ctx context.Context) (string, error) {
	return b.currentHeader(ctx)
}

func benignBackend() backendStub {
	return backendStub{
		handleEvent: func(_ context.Context, event protocol.Event) (EventResult, error) {
			return EventResult{Effect: protocol.Effect{Put: event.Key}}, nil
		},
		loadGeneration: func(_ context.Context, request LoadRequest) ([]byte, error) {
			return []byte{byte(request.Generation)}, nil
		},
		resolvePreview: func(_ context.Context, current []byte) (protocol.ResolvedCandidate, error) {
			return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte("/tmp/preview"), Size: 7, Mode: 0o644}, nil
		},
		recordPreview: func(context.Context, PreviewRequest) error { return nil },
		currentHeader: func(context.Context) (string, error) { return "/", nil },
	}
}

func fixedToken(value byte) Token {
	var token Token
	for i := range token {
		token[i] = value
	}
	return token
}

func startServer(t *testing.T, backend Backend) (*Server, *Client) {
	t.Helper()
	token := fixedToken(7)
	server, err := Listen(context.Background(), token, backend)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	client := newClient(server.Address(), token)
	t.Cleanup(func() {
		closeSessionIPC(t, server, "test cleanup", nil)
	})
	return server, client
}

func TestTokenAndEnvironmentAreStrict(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token.String())
	if err != nil || len(decoded) != 32 {
		t.Fatalf("token encoding len=%d err=%v", len(decoded), err)
	}

	values := map[string]string{"SHELL_PICKER_ADDR": "http://127.0.0.1:1234", "SHELL_PICKER_TOKEN": fixedToken(3).String()}
	client, err := NewClientFromEnv(func(key string) string { return values[key] })
	if err != nil || client.address != values["SHELL_PICKER_ADDR"] {
		t.Fatalf("NewClientFromEnv client=%+v err=%v", client, err)
	}
	for _, bad := range []map[string]string{
		{"SHELL_PICKER_ADDR": values["SHELL_PICKER_ADDR"]},
		{"SHELL_PICKER_ADDR": values["SHELL_PICKER_ADDR"], "SHELL_PICKER_TOKEN": base64.StdEncoding.EncodeToString(make([]byte, 32))},
		{"SHELL_PICKER_ADDR": values["SHELL_PICKER_ADDR"], "SHELL_PICKER_TOKEN": fixedToken(3).String() + "="},
	} {
		if _, err := NewClientFromEnv(func(key string) string { return bad[key] }); err == nil {
			t.Fatalf("accepted environment %+v", bad)
		}
	}
}

func TestClientAcceptsOnlyExactLoopbackURL(t *testing.T) {
	accepted := []string{"http://127.0.0.1:1", "http://127.0.0.1:65535"}
	rejected := []string{
		"https://127.0.0.1:1", "http://localhost:1", "http://127.0.0.2:1", "http://user@127.0.0.1:1",
		"http://127.0.0.1:0", "http://127.0.0.1:01", "http://127.0.0.1:65536", "http://127.0.0.1:1/",
		"http://127.0.0.1:1/x", "http://127.0.0.1:1?q=1", "http://127.0.0.1:1#x", "HTTP://127.0.0.1:1",
		"http://0177.0.0.1:1", "http://127.0.0.1:+1", "http://127.0.0.1:1%20",
	}
	for _, raw := range accepted {
		if _, err := parseEndpoint(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
	for _, raw := range rejected {
		if _, err := parseEndpoint(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestServerAuthenticatesAndPreservesOpaqueBytes(t *testing.T) {
	seen := make(chan protocol.Event, 1)
	backend := benignBackend()
	backend.handleEvent = func(_ context.Context, event protocol.Event) (EventResult, error) {
		seen <- event
		return EventResult{Effect: protocol.Effect{Put: "ok"}}, nil
	}
	_, client := startServer(t, backend)

	response, err := client.Event(context.Background(), eventRequest())
	if err != nil || response.Effect.Put != "ok" {
		t.Fatalf("Event response=%+v err=%v", response, err)
	}
	event := awaitSessionIPC(t, seen, "opaque event delivery")
	if !bytes.Equal(event.Query, []byte{0xff, 'q'}) || !bytes.Equal(event.CurrentItem, []byte("file\titem\tL3RtcC94")) {
		t.Fatalf("event bytes query=%x current=%x", event.Query, event.CurrentItem)
	}

	client.token = fixedToken(9)
	_, err = client.Event(context.Background(), eventRequest())
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), client.token.String()) {
		t.Fatalf("forged bearer error=%v", err)
	}
}

func TestClientFinalizeEventCallsBackendAfterEventResponse(t *testing.T) {
	finalized := make(chan EventFinalizeRequest, 1)
	backend := benignBackend()
	backend.finalizeEvent = func(request EventFinalizeRequest) { finalized <- request }
	effect := protocol.Effect{Abort: true}
	backend.handleEvent = func(context.Context, protocol.Event) (EventResult, error) {
		return EventResult{Effect: effect, EventID: 1}, nil
	}
	_, client := startServer(t, backend)

	response, err := client.Event(context.Background(), eventRequest())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-finalized:
		t.Fatalf("event response finalized before callback acknowledgement: %+v", got)
	default:
	}
	if err := client.FinalizeEvent(context.Background(), EventFinalizeRequest{EventID: response.EventID, Applied: true}); err != nil {
		t.Fatalf("FinalizeEvent: %v", err)
	}
	select {
	case got := <-finalized:
		if got.EventID != 1 || !got.Applied {
			t.Fatalf("finalized effect=%+v want=%+v", got, effect)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend finalizer was not called")
	}
}

func TestFinalizeRouteAcceptsOnlyAnExactNonzeroEventIDAndAppliedFlag(t *testing.T) {
	server, _ := startServer(t, benignBackend())
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "applied", body: `{"event_id":7,"applied":true}`, want: http.StatusNoContent},
		{name: "unapplied", body: `{"event_id":8,"applied":false}`, want: http.StatusNoContent},
		{name: "zero id", body: `{"event_id":0,"applied":true}`, want: http.StatusBadRequest},
		{name: "missing applied", body: `{"event_id":7}`, want: http.StatusBadRequest},
		{name: "missing id", body: `{"applied":true}`, want: http.StatusBadRequest},
		{name: "effect authority rejected", body: `{"event_id":7,"applied":true,"effect":{"abort":true}}`, want: http.StatusBadRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := rawRequest(t, server.Address()+"/v1/event/finalize", fixedToken(7), http.MethodPost, "application/json", []byte(test.body))
			defer response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.want)
			}
		})
	}
}

func TestFinalizeRouteUsesTheSameBearerAuthentication(t *testing.T) {
	server, _ := startServer(t, benignBackend())
	body := []byte(`{"event_id":7,"applied":true}`)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.Address()+"/v1/event/finalize", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestClientRejectsZeroFinalizeIDBeforeTransport(t *testing.T) {
	client := newClient("http://127.0.0.1:1", fixedToken(7))
	if err := client.FinalizeEvent(context.Background(), EventFinalizeRequest{Applied: true}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("FinalizeEvent zero ID error=%v, want %v", err, ErrBadRequest)
	}
}

func TestServerFinalizesEventWhenResponseWriteFails(t *testing.T) {
	finalized := make(chan EventFinalizeRequest, 1)
	backend := benignBackend()
	backend.finalizeEvent = func(request EventFinalizeRequest) { finalized <- request }
	backend.handleEvent = func(context.Context, protocol.Event) (EventResult, error) {
		return EventResult{Effect: protocol.Effect{Abort: true}, EventID: 2}, nil
	}
	body, err := json.Marshal(eventRequest())
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{backend: backend}
	server.handleEvent(&eventResponseErrorWriter{header: make(http.Header)}, httptest.NewRequest(http.MethodPost, "/v1/event", bytes.NewReader(body)), body)
	select {
	case got := <-finalized:
		if got.EventID != 2 || got.Applied {
			t.Fatalf("finalized effect=%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response failure did not finalize event")
	}
}

func TestDisplayReturnsCurrentHeaderFromStrictEmptyRequest(t *testing.T) {
	backend := benignBackend()
	backend.currentHeader = func(context.Context) (string, error) { return "/work/", nil }
	_, client := startServer(t, backend)
	response, err := client.Display(context.Background())
	if err != nil || response.Header != "/work/" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestServerEnforcesMethodContentTypeAndStrictJSON(t *testing.T) {
	server, _ := startServer(t, benignBackend())
	token := fixedToken(7)
	cases := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		want        int
	}{
		{"method", http.MethodGet, "/v1/event", "application/json", `{}`, http.StatusMethodNotAllowed},
		{"route", http.MethodPost, "/v1/missing", "application/json", `{}`, http.StatusNotFound},
		{"content type", http.MethodPost, "/v1/event", "text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"content type parameter", http.MethodPost, "/v1/event", "application/json; charset=utf-8", `{}`, http.StatusUnsupportedMediaType},
		{"unknown field", http.MethodPost, "/v1/event", "application/json", `{"opcode":"es","key":"","query_base64":"","current_item_base64":"","extra":1}`, http.StatusBadRequest},
		{"null", http.MethodPost, "/v1/event", "application/json", `null`, http.StatusBadRequest},
		{"second object", http.MethodPost, "/v1/event", "application/json", `{"opcode":"es","key":"","query_base64":"","current_item_base64":""}{}`, http.StatusBadRequest},
		{"raw base64", http.MethodPost, "/v1/event", "application/json", `{"opcode":"es","key":"","query_base64":"eA","current_item_base64":""}`, http.StatusBadRequest},
		{"noncanonical base64", http.MethodPost, "/v1/event", "application/json", `{"opcode":"es","key":"","query_base64":"eB==","current_item_base64":""}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := rawRequest(t, server.Address()+tc.path, token, tc.method, tc.contentType, []byte(tc.body))
			defer response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, tc.want)
			}
		})
	}
}

func TestServerAppliesHardBodyLimit(t *testing.T) {
	server, _ := startServer(t, benignBackend())
	response := rawRequest(t, server.Address()+"/v1/event", fixedToken(7), http.MethodPost, "application/json", bytes.Repeat([]byte{'x'}, (64<<10)+1))
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestRoutesMapResponsesAndErrors(t *testing.T) {
	previewPath := filepath.Join(t.TempDir(), "preview.bin")
	if err := os.WriteFile(previewPath, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := benignBackendForPath([]byte(previewPath))
	backend.loadGeneration = func(_ context.Context, request LoadRequest) ([]byte, error) {
		if request.Generation == 9 {
			return nil, session.ErrStaleGeneration
		}
		return []byte{0, 0xff, byte(request.Generation)}, nil
	}
	backend.resolvePreview = func(_ context.Context, current []byte) (protocol.ResolvedCandidate, error) {
		if bytes.Equal(current, []byte("missing")) {
			return protocol.ResolvedCandidate{}, session.ErrUnknownRecord
		}
		return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(previewPath), Size: 8, ModTimeUnixNano: 9, Mode: 0o600}, nil
	}
	_, client := startServer(t, backend)

	loaded, err := client.Load(context.Background(), LoadRequest{Generation: 4})
	if err != nil || !bytes.Equal(loaded, []byte{0, 0xff, 4}) {
		t.Fatalf("Load=%x err=%v", loaded, err)
	}
	if _, err := client.Load(context.Background(), LoadRequest{Generation: 9}); !errors.Is(err, ErrInvalidLoad) {
		t.Fatalf("stale load err=%v", err)
	}
	preview, err := client.ResolvePreview(context.Background(), PreviewRequest{Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("record"))})
	wantPathBase64 := base64.StdEncoding.EncodeToString([]byte(previewPath))
	if err != nil || preview.PathBase64 != wantPathBase64 || preview.Kind != protocol.KindFile {
		t.Fatalf("preview=%+v err=%v want_path_base64=%q", preview, err, wantPathBase64)
	}
	_, err = client.ResolvePreview(context.Background(), PreviewRequest{Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("missing"))})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing preview err=%v", err)
	}
}

func TestPreviewValidationAndTelemetry(t *testing.T) {
	previewPath := filepath.Join(t.TempDir(), "preview.bin")
	if err := os.WriteFile(previewPath, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorded := make(chan PreviewRequest, 2)
	backend := benignBackendForPath([]byte(previewPath))
	backend.resolvePreview = func(_ context.Context, current []byte) (protocol.ResolvedCandidate, error) {
		if bytes.Equal(current, []byte("virtual")) {
			return protocol.ResolvedCandidate{}, session.ErrUnknownRecord
		}
		return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(previewPath)}, nil
	}
	backend.recordPreview = func(_ context.Context, request PreviewRequest) error {
		recorded <- request
		return nil
	}
	server, client := startServer(t, backend)
	current := base64.StdEncoding.EncodeToString([]byte("record"))
	valid := []PreviewRequest{
		{Phase: "started", CurrentItemBase64: current, Renderer: "bat"},
		{Phase: "finished", CurrentItemBase64: current, Renderer: "bat", DurationUS: 12, ChildStarts: 3, MaxLiveChildren: 1, Outcome: "ok"},
	}
	for _, request := range valid {
		if err := client.RecordPreview(context.Background(), request); err != nil {
			t.Fatalf("RecordPreview(%s): %v", request.Phase, err)
		}
		if got := awaitPreviewRecord(t, recorded); got.Phase != request.Phase {
			t.Fatalf("recorded phase=%q", got.Phase)
		}
	}

	invalid := []PreviewRequest{
		{Phase: "bad", CurrentItemBase64: current},
		{Phase: "resolve", CurrentItemBase64: current, Renderer: "control"},
		{Phase: "started", CurrentItemBase64: current, DurationUS: 1},
		{Phase: "finished", CurrentItemBase64: current, DurationUS: -1},
		{Phase: "finished", CurrentItemBase64: current, ChildStarts: 4},
		{Phase: "finished", CurrentItemBase64: current, MaxLiveChildren: 2},
		{Phase: "finished", CurrentItemBase64: current, ChildStarts: 0, MaxLiveChildren: 1},
	}
	for _, request := range invalid {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response := rawRequest(t, server.Address()+"/v1/preview", fixedToken(7), http.MethodPost, "application/json", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("request=%+v status=%d", request, response.StatusCode)
		}
	}

	t.Run("backend rejects telemetry", func(t *testing.T) {
		backend := benignBackendForPath([]byte(previewPath))
		backend.recordPreview = func(context.Context, PreviewRequest) error {
			return errors.New("telemetry rejected")
		}
		server, _ := startServer(t, backend)
		body, err := json.Marshal(valid[0])
		if err != nil {
			t.Fatal(err)
		}
		response := rawRequest(t, server.Address()+"/v1/preview", fixedToken(7), http.MethodPost, "application/json", body)
		response.Body.Close()
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d want=%d", response.StatusCode, http.StatusInternalServerError)
		}
	})
}

func TestDisplayContextCancellationFlowsToBackend(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan error, 1)
	backend := benignBackend()
	backend.currentHeader = func(ctx context.Context) (string, error) {
		close(entered)
		<-ctx.Done()
		cancelled <- context.Cause(ctx)
		return "", context.Cause(ctx)
	}
	_, client := startServer(t, backend)
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Display(ctx)
		done <- err
	}()
	awaitSessionIPC(t, entered, "display backend entry before caller cancellation")
	cancel(errors.New("caller stopped"))
	if err := awaitSessionIPC(t, cancelled, "display backend cancellation"); err == nil {
		t.Fatal("backend did not receive caller cancellation")
	}
	if err := awaitSessionIPC(t, done, "display request completion after caller cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled display err=%v", err)
	}
}

func TestPreviewTelemetryUsesIndependentSoftTimeout(t *testing.T) {
	requestObserved := make(chan struct{})
	previewPath := filepath.Join(t.TempDir(), "preview.bin")
	if err := os.WriteFile(previewPath, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := benignBackendForPath([]byte(previewPath))
	backend.recordPreview = func(ctx context.Context, _ PreviewRequest) error {
		close(requestObserved)
		<-ctx.Done()
		return context.Cause(ctx)
	}
	_, client := startServer(t, backend)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := client.RecordPreview(cancelled, PreviewRequest{
		Phase: "started", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("record")), Renderer: "native",
	})
	if err != nil {
		t.Fatalf("soft telemetry err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("telemetry blocked %v", elapsed)
	}
	awaitSessionIPC(t, requestObserved, "preview telemetry backend entry")
}
