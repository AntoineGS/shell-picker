package sessionipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestServerRejectsNoncanonicalRouteTargetsBeforeBackend(t *testing.T) {
	var calls atomic.Int32
	backend := benignBackend()
	backend.handleEvent = func(context.Context, protocol.Event) (protocol.Effect, error) {
		calls.Add(1)
		return protocol.Effect{}, nil
	}
	server, _ := startServer(t, backend)
	body, err := json.Marshal(eventRequest())
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"/v1/event?x=1", "/v1%2fevent", "/v1%2Fevent", "/v1/event/", "//v1/event", "/v1//event", "/%76%31/event",
	} {
		t.Run(target, func(t *testing.T) {
			response := rawRequest(t, server.Address()+target, fixedToken(7), http.MethodPost, "application/json", body)
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("target=%q status=%d", target, response.StatusCode)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("noncanonical routes invoked backend %d times", calls.Load())
	}
}

func TestDisplayRejectsInvalidRequestsBeforeBackend(t *testing.T) {
	var calls atomic.Int32
	backend := benignBackend()
	backend.currentHeader = func(context.Context) (string, error) {
		calls.Add(1)
		return "/work/", nil
	}
	server, _ := startServer(t, backend)
	tests := []struct {
		name       string
		target     string
		body       []byte
		authorized bool
		want       int
	}{
		{name: "query", target: "/v1/display?x=1", body: []byte(`{}`), authorized: true, want: http.StatusNotFound},
		{name: "trailing slash", target: "/v1/display/", body: []byte(`{}`), authorized: true, want: http.StatusNotFound},
		{name: "nonempty object", target: "/v1/display", body: []byte(`{"extra":1}`), authorized: true, want: http.StatusBadRequest},
		{name: "missing authorization", target: "/v1/display", body: []byte(`{}`), want: http.StatusUnauthorized},
		{name: "oversized body", target: "/v1/display", body: bytes.Repeat([]byte{'x'}, (64<<10)+1), authorized: true, want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.Address()+test.target, bytes.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			if test.authorized {
				request.Header.Set("Authorization", "Bearer "+fixedToken(7).String())
			}
			response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.want)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid display requests invoked backend %d times", calls.Load())
	}
}

func TestServerAcceptsOnlyOneExactRawAuthorizationValue(t *testing.T) {
	var calls atomic.Int32
	backend := benignBackend()
	backend.handleEvent = func(context.Context, protocol.Event) (protocol.Effect, error) {
		calls.Add(1)
		return protocol.Effect{}, nil
	}
	server, _ := startServer(t, backend)
	token := fixedToken(7).String()

	tests := []struct {
		name    string
		headers []string
		want    int
	}{
		{"exact", []string{"Authorization: Bearer " + token}, http.StatusOK},
		{"missing", nil, http.StatusUnauthorized},
		{"duplicate valid", []string{"Authorization: Bearer " + token, "Authorization: Bearer " + token}, http.StatusUnauthorized},
		{"valid then invalid", []string{"Authorization: Bearer " + token, "Authorization: Bearer forged"}, http.StatusUnauthorized},
		{"invalid then valid", []string{"Authorization: Bearer forged", "Authorization: Bearer " + token}, http.StatusUnauthorized},
		{"comma joined", []string{"Authorization: Bearer " + token + ", Bearer " + token}, http.StatusUnauthorized},
		{"lowercase bearer", []string{"Authorization: bearer " + token}, http.StatusUnauthorized},
		{"alternate scheme", []string{"Authorization: Token " + token}, http.StatusUnauthorized},
		{"leading whitespace", []string{"Authorization:  Bearer " + token}, http.StatusUnauthorized},
		{"trailing whitespace", []string{"Authorization: Bearer " + token + " "}, http.StatusUnauthorized},
		{"doubled whitespace", []string{"Authorization: Bearer  " + token}, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := rawWireEvent(t, server.Address(), test.headers)
			response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.want)
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatalf("backend calls=%d want=1", calls.Load())
	}
}

func TestServerRejectsChunkedRequestAt64KiBPlusOne(t *testing.T) {
	var calls atomic.Int32
	backend := benignBackend()
	backend.handleEvent = func(context.Context, protocol.Event) (protocol.Effect, error) {
		calls.Add(1)
		return protocol.Effect{}, nil
	}
	server, _ := startServer(t, backend)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.Address()+"/v1/event", bytes.NewReader(bytes.Repeat([]byte{'x'}, (64<<10)+1)))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	request.Header.Set("Authorization", "Bearer "+fixedToken(7).String())
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || calls.Load() != 0 {
		t.Fatalf("status=%d backend calls=%d", response.StatusCode, calls.Load())
	}
}

func TestServerRejectsBackendReturnedVirtualPreview(t *testing.T) {
	backend := benignBackend()
	backend.resolvePreview = func(context.Context, []byte) (protocol.ResolvedCandidate, error) {
		return protocol.ResolvedCandidate{Kind: protocol.KindVirtual, Path: []byte(protocol.VirtualDrivesTarget)}, nil
	}
	_, client := startServer(t, backend)
	response, err := client.ResolvePreview(context.Background(), PreviewRequest{
		Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("authorized-record")),
	})
	if !errors.Is(err, ErrNotFound) || response.PathBase64 != "" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestFinishedTelemetryExactChildBounds(t *testing.T) {
	var recorded atomic.Int32
	previewPath := filepath.Join(t.TempDir(), "preview.bin")
	if err := os.WriteFile(previewPath, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := benignBackendForPath([]byte(previewPath))
	backend.recordPreview = func(context.Context, PreviewRequest) error {
		recorded.Add(1)
		return nil
	}
	server, _ := startServer(t, backend)
	current := base64.StdEncoding.EncodeToString([]byte("authorized-record"))
	tests := []struct {
		name       string
		starts     int
		maxLive    int
		wantStatus int
	}{
		{"at limit", 3, 1, http.StatusNoContent},
		{"starts over", 4, 1, http.StatusBadRequest},
		{"live over", 3, 2, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(PreviewRequest{
				Phase: "finished", CurrentItemBase64: current, Renderer: "bat", DurationUS: 12,
				ChildStarts: test.starts, MaxLiveChildren: test.maxLive, Outcome: "ok",
			})
			if err != nil {
				t.Fatal(err)
			}
			response := rawRequest(t, server.Address()+"/v1/preview", fixedToken(7), http.MethodPost, "application/json", body)
			response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.wantStatus)
			}
		})
	}
	if recorded.Load() != 1 {
		t.Fatalf("recorded=%d want=1", recorded.Load())
	}
}

func TestServerCloseCancelsCooperativeBackendAndJoinsHandler(t *testing.T) {
	called := make(chan struct{})
	returned := make(chan struct{})
	backend := benignBackend()
	backend.handleEvent = func(ctx context.Context, _ protocol.Event) (protocol.Effect, error) {
		close(called)
		<-ctx.Done()
		close(returned)
		return protocol.Effect{}, context.Cause(ctx)
	}
	server, client := startServer(t, backend)
	requestDone := make(chan error, 1)
	go func() {
		_, err := client.Event(context.Background(), eventRequest())
		requestDone <- err
	}()
	<-called
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("backend had not returned before Close")
	}
	select {
	case <-requestDone:
	case <-closeCtx.Done():
		t.Fatal("request handler did not join within close bound")
	}
}

func rawWireEvent(t *testing.T, address string, headers []string) *http.Response {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(eventRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp4", parsed.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := "POST /v1/event HTTP/1.1\r\nHost: " + parsed.Host + "\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n"
	if len(headers) != 0 {
		request += strings.Join(headers, "\r\n") + "\r\n"
	}
	request += "Connection: close\r\n\r\n" + string(body)
	if _, err := io.WriteString(connection, request); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	response.Body = &connectionBody{ReadCloser: response.Body, connection: connection}
	return response
}

type connectionBody struct {
	io.ReadCloser
	connection net.Conn
}

func (body *connectionBody) Close() error {
	bodyErr := body.ReadCloser.Close()
	connectionErr := body.connection.Close()
	if bodyErr != nil {
		return bodyErr
	}
	return connectionErr
}
