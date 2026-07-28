package sessionipc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestClientClosesOverlimitBodyWithoutReusableTransport(t *testing.T) {
	body := &trackedResponseBody{Reader: bytes.NewReader(make([]byte, maxTelemetryResponseBytes+1))}
	client := &Client{}
	_, err := client.readResponse(&http.Response{Body: body}, maxTelemetryResponseBytes)
	if !errors.Is(err, ErrInternal) || !body.closed {
		t.Fatalf("err=%v closed=%v", err, body.closed)
	}
}

func TestClientBoundsEveryResponseClassAtLimitAndLimitPlusOne(t *testing.T) {
	tests := []struct {
		name        string
		limit       int64
		status      int
		contentType string
		prefix      string
		fill        byte
		call        func(context.Context, *Client) (int, error)
		atLimitErr  error
	}{
		{
			name: "event JSON", limit: 64 << 10, status: http.StatusOK, contentType: "application/json",
			prefix: `{"effect":{}}`, fill: ' ',
			call: func(ctx context.Context, client *Client) (int, error) {
				_, err := client.Event(ctx, eventRequest())
				return 0, err
			},
		},
		{
			name: "preview JSON", limit: 64 << 10, status: http.StatusOK, contentType: "application/json",
			prefix: `{"kind":"file","path_base64":"L3RtcC94","size":0,"mod_time_unix_nano":0,"mode":0}`, fill: ' ',
			call: func(ctx context.Context, client *Client) (int, error) {
				_, err := client.ResolvePreview(ctx, PreviewRequest{Phase: "resolve", CurrentItemBase64: "eA=="})
				return 0, err
			},
		},
		{
			name: "JSON error", limit: 64 << 10, status: http.StatusUnauthorized, contentType: "application/json",
			prefix: `{"error":"Unauthorized"}`, fill: ' ',
			call: func(ctx context.Context, client *Client) (int, error) {
				_, err := client.Event(ctx, eventRequest())
				return 0, err
			},
			atLimitErr: ErrUnauthorized,
		},
		{
			name: "load octet stream", limit: 64 << 20, status: http.StatusOK, contentType: "application/octet-stream",
			call: func(ctx context.Context, client *Client) (int, error) {
				data, err := client.Load(ctx, LoadRequest{Generation: 1})
				return len(data), err
			},
		},
		{
			name: "telemetry response", limit: 1 << 10, status: http.StatusOK, contentType: "application/octet-stream",
			call: func(ctx context.Context, client *Client) (int, error) {
				err := client.RecordPreview(ctx, PreviewRequest{Phase: "started", CurrentItemBase64: "eA==", Renderer: "native"})
				return 0, err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name+" at limit", func(t *testing.T) {
			accepted, client, closeServer := startRogueResponseServer(t, test.status, test.contentType, test.prefix, test.fill, test.limit)
			defer closeServer()
			for range 2 {
				length, err := test.call(context.Background(), client)
				if !errors.Is(err, test.atLimitErr) {
					t.Fatalf("err=%v want=%v", err, test.atLimitErr)
				}
				if test.name == "load octet stream" && int64(length) != test.limit {
					t.Fatalf("load length=%d want=%d", length, test.limit)
				}
			}
			if accepted.Load() != 1 {
				t.Fatalf("at-limit connection count=%d want=1", accepted.Load())
			}
		})
		t.Run(test.name+" over limit", func(t *testing.T) {
			accepted, client, closeServer := startRogueResponseServer(t, test.status, test.contentType, test.prefix, test.fill, test.limit+1)
			defer closeServer()
			for range 2 {
				length, err := test.call(context.Background(), client)
				if test.name != "telemetry response" && !errors.Is(err, ErrInternal) {
					t.Fatalf("overlimit err=%v", err)
				}
				if length != 0 {
					t.Fatalf("overlimit returned %d bytes", length)
				}
			}
			if accepted.Load() != 2 {
				t.Fatalf("overlimit connection count=%d want=2", accepted.Load())
			}
		})
	}
}

func startRogueResponseServer(t *testing.T, status int, contentType, prefix string, fill byte, size int64) (*atomic.Int32, *Client, func()) {
	t.Helper()
	if int64(len(prefix)) > size {
		t.Fatalf("prefix length=%d exceeds response size=%d", len(prefix), size)
	}
	var accepted atomic.Int32
	handler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", contentType)
		response.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		response.WriteHeader(status)
		if _, err := io.WriteString(response, prefix); err != nil {
			return
		}
		_, _ = io.CopyN(response, fillReader{value: fill}, size-int64(len(prefix)))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted.Add(1)
		}
	}
	server.Start()
	client := newClient(server.URL, fixedToken(7))
	return &accepted, client, func() {
		client.client.Transport.(*http.Transport).CloseIdleConnections()
		server.Close()
	}
}

type fillReader struct {
	value byte
}

type trackedResponseBody struct {
	*bytes.Reader
	closed bool
}

func (body *trackedResponseBody) Close() error {
	body.closed = true
	return nil
}

func (reader fillReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = reader.value
	}
	return len(buffer), nil
}
