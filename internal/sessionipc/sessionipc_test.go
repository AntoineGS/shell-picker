package sessionipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

type backendStub struct {
	handleEvent    func(context.Context, protocol.Event) (protocol.Effect, error)
	loadGeneration func(context.Context, uint64) ([]byte, error)
	resolvePreview func(context.Context, []byte) (protocol.ResolvedCandidate, error)
	recordPreview  func(context.Context, PreviewRequest) error
	currentHeader  func(context.Context) (string, error)
}

func (b backendStub) HandleEvent(ctx context.Context, event protocol.Event) (protocol.Effect, error) {
	return b.handleEvent(ctx, event)
}

func (b backendStub) LoadGeneration(ctx context.Context, generation uint64) ([]byte, error) {
	return b.loadGeneration(ctx, generation)
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
		handleEvent: func(_ context.Context, event protocol.Event) (protocol.Effect, error) {
			return protocol.Effect{Put: event.Key}, nil
		},
		loadGeneration: func(_ context.Context, generation uint64) ([]byte, error) {
			return []byte{byte(generation)}, nil
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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return server, client
}

func eventRequest() EventRequest {
	return EventRequest{
		Opcode:            protocol.OpEscape,
		Key:               "escape",
		QueryBase64:       base64.StdEncoding.EncodeToString([]byte{0xff, 'q'}),
		CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("file\titem\tL3RtcC94")),
	}
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
	backend.handleEvent = func(_ context.Context, event protocol.Event) (protocol.Effect, error) {
		seen <- event
		return protocol.Effect{Put: "ok"}, nil
	}
	_, client := startServer(t, backend)

	response, err := client.Event(context.Background(), eventRequest())
	if err != nil || response.Effect.Put != "ok" {
		t.Fatalf("Event response=%+v err=%v", response, err)
	}
	event := <-seen
	if !bytes.Equal(event.Query, []byte{0xff, 'q'}) || !bytes.Equal(event.CurrentItem, []byte("file\titem\tL3RtcC94")) {
		t.Fatalf("event bytes query=%x current=%x", event.Query, event.CurrentItem)
	}

	client.token = fixedToken(9)
	_, err = client.Event(context.Background(), eventRequest())
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), client.token.String()) {
		t.Fatalf("forged bearer error=%v", err)
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
	backend := benignBackend()
	backend.loadGeneration = func(_ context.Context, generation uint64) ([]byte, error) {
		if generation == 9 {
			return nil, session.ErrStaleGeneration
		}
		return []byte{0, 0xff, byte(generation)}, nil
	}
	backend.resolvePreview = func(_ context.Context, current []byte) (protocol.ResolvedCandidate, error) {
		if bytes.Equal(current, []byte("missing")) {
			return protocol.ResolvedCandidate{}, session.ErrUnknownRecord
		}
		return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte{'/', 0xff, 'p'}, Size: 8, ModTimeUnixNano: 9, Mode: 0o600}, nil
	}
	_, client := startServer(t, backend)

	loaded, err := client.Load(context.Background(), LoadRequest{Generation: 4})
	if err != nil || !bytes.Equal(loaded, []byte{0, 0xff, 4}) {
		t.Fatalf("Load=%x err=%v", loaded, err)
	}
	if _, err := client.Load(context.Background(), LoadRequest{Generation: 9}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale load err=%v", err)
	}
	preview, err := client.ResolvePreview(context.Background(), PreviewRequest{Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("record"))})
	if err != nil || preview.PathBase64 != "L/9w" || preview.Kind != protocol.KindFile {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	_, err = client.ResolvePreview(context.Background(), PreviewRequest{Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("missing"))})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing preview err=%v", err)
	}
}

func TestPreviewValidationAndTelemetry(t *testing.T) {
	recorded := make(chan PreviewRequest, 2)
	backend := benignBackend()
	backend.resolvePreview = func(_ context.Context, current []byte) (protocol.ResolvedCandidate, error) {
		if bytes.Equal(current, []byte("virtual")) {
			return protocol.ResolvedCandidate{}, session.ErrUnknownRecord
		}
		return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte("/tmp/x")}, nil
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
		if got := <-recorded; got.Phase != request.Phase {
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
	<-entered
	cancel(errors.New("caller stopped"))
	if err := <-cancelled; err == nil {
		t.Fatal("backend did not receive caller cancellation")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled display err=%v", err)
	}
}

func TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins(t *testing.T) {
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	exited := make(chan struct{}, 16)
	backend := benignBackend()
	backend.currentHeader = func(ctx context.Context) (string, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		exited <- struct{}{}
		return "", context.Cause(ctx)
	}
	server, client := startServer(t, backend)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.Display(context.Background())
		}()
	}
	for range 16 {
		<-entered
	}
	if _, err := client.Display(context.Background()); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("seventeenth request err=%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for range 16 {
		<-exited
	}
	wg.Wait()
	if _, err := client.Display(context.Background()); err == nil {
		t.Fatal("closed endpoint accepted request")
	}
	close(release)
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestPreviewTelemetryUsesIndependentSoftTimeout(t *testing.T) {
	requestObserved := make(chan struct{})
	backend := benignBackend()
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
	<-requestObserved
}

func rawRequest(t *testing.T, url string, token Token, method, contentType string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token.String())
	request.Header.Set("Content-Type", contentType)
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
