package fzfsidecar

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestReadBodyAcceptsLimitAndRejectsLimitPlusOneWhileClosing(t *testing.T) {
	for _, size := range []int{maxResponseBytes, maxResponseBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			body := &trackedBody{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, size))}
			client := &client{}
			got, err := client.readBody(&http.Response{Body: body})
			if size == maxResponseBytes {
				if err != nil || len(got) != size {
					t.Fatalf("readBody at limit: len=%d err=%v", len(got), err)
				}
			} else if !errors.Is(err, errResponseTooBig) || got != nil {
				t.Fatalf("readBody over limit: len=%d err=%v", len(got), err)
			}
			if body.closes.Load() != 1 {
				t.Fatalf("body close count = %d, want 1", body.closes.Load())
			}
		})
	}
}

func TestClientRequestsUseExactURLKeyAndContentPolicy(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Method == http.MethodGet {
			if request.URL.String() != "http://127.0.0.1:12345/?limit=100&offset=0" {
				t.Errorf("GET URL = %q", request.URL.String())
			}
			if request.Header.Get("X-Api-Key") != "secret" {
				t.Errorf("GET key = %q", request.Header.Get("X-Api-Key"))
			}
			if strings.Contains(request.URL.String(), "secret") {
				t.Error("GET URL contains API key")
			}
			return response(http.StatusOK, "application/json; charset=utf-8", `{"totalCount":42,"matchCount":7,"selected":[]}`), nil
		}
		if request.Method != http.MethodPost {
			t.Errorf("method = %q", request.Method)
		}
		if request.URL.String() != "http://127.0.0.1:12345/" {
			t.Errorf("POST URL = %q", request.URL.String())
		}
		if request.Header.Get("X-Api-Key") != "secret" {
			t.Errorf("POST key = %q", request.Header.Get("X-Api-Key"))
		}
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read POST body: %v", err)
		}
		if string(body) != "change-list-label:7/42 (2)" {
			t.Errorf("POST body = %q", body)
		}
		if request.ContentLength != int64(len(body)) {
			t.Errorf("POST content length = %d, want %d", request.ContentLength, len(body))
		}
		return response(http.StatusOK, "text/plain", "ok"), nil
	})
	httpClient := &http.Client{Transport: transport, CheckRedirect: rejectRedirect}
	client := newClient("127.0.0.1:12345", "secret", httpClient, nil, protocol.PickerCP)

	if state, err := client.getState(context.Background()); err != nil || state.formatted != "7/42" {
		t.Fatalf("getState = %q, %v", state.formatted, err)
	}
	state, err := decodeState([]byte(`{"totalCount":42,"matchCount":7,"selected":[{},{}]}`), protocol.PickerCP)
	if err != nil {
		t.Fatalf("decode state for POST: %v", err)
	}
	if err := client.postState(context.Background(), state); err != nil {
		t.Fatalf("postState: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("request count = %d, want 2", calls.Load())
	}
}

func TestDefaultTransportDisablesProxy(t *testing.T) {
	httpClient, _ := newHTTPClient(defaultSessionOptions())
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default transport has a proxy")
	}
	if !transport.DisableKeepAlives {
		t.Fatal("default transport reuses idle connections; fzf listen may close them between requests")
	}
}

func TestClientRejectsRedirectsWithoutFollowingThem(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) != 1 {
			return nil, errors.New("redirect was followed")
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header: http.Header{
				"Location":     []string{"http://127.0.0.1:54321/"},
				"Content-Type": []string{"text/plain"},
			},
			Body: io.NopCloser(strings.NewReader("redirect")),
		}, nil
	})
	httpClient, idleCloser := newHTTPClient(sessionOptions{transport: transport})
	client := newClient("127.0.0.1:12345", "secret", httpClient, idleCloser, protocol.PickerCD)
	if _, err := client.getState(context.Background()); !errors.Is(err, errInvalidStatus) {
		t.Fatalf("getState error = %v, want invalid status", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("round trips = %d, want 1", got)
	}
}

func TestPostRejectsStatusAndClosesResponseBody(t *testing.T) {
	body := &trackedBody{reader: bytes.NewReader([]byte("failure"))}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: body}, nil
	})
	httpClient, idleCloser := newHTTPClient(sessionOptions{transport: transport})
	client := newClient("127.0.0.1:12345", "secret", httpClient, idleCloser, protocol.PickerCD)
	if err := client.postLabel(context.Background(), "7/42"); !errors.Is(err, errInvalidStatus) {
		t.Fatalf("postLabel error = %v, want invalid status", err)
	}
	if got := body.closes.Load(); got != 1 {
		t.Fatalf("body close count = %d, want 1", got)
	}
}

func TestPostResponseUsesTheSameBoundedBodyPolicy(t *testing.T) {
	for _, size := range []int{maxResponseBytes, maxResponseBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			body := &trackedBody{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, size))}
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
			})
			httpClient, idleCloser := newHTTPClient(sessionOptions{transport: transport})
			client := newClient("127.0.0.1:12345", "secret", httpClient, idleCloser, protocol.PickerCD)
			err := client.postLabel(context.Background(), "7/42")
			if size == maxResponseBytes {
				if err != nil {
					t.Fatalf("postLabel at limit: %v", err)
				}
			} else if !errors.Is(err, errResponseTooBig) {
				t.Fatalf("postLabel over limit error = %v, want too large", err)
			}
			if got := body.closes.Load(); got != 1 {
				t.Fatalf("body close count = %d, want 1", got)
			}
		})
	}
}

func TestClientKeepsTypedPostTransportFailuresRawAndPost503Transient(t *testing.T) {
	tests := []struct {
		name          string
		cause         error
		status        int
		wantTransient bool
	}{
		{name: "EOF", cause: io.EOF},
		{name: "unexpected EOF", cause: io.ErrUnexpectedEOF},
		{name: "connection reset", cause: syscall.ECONNRESET},
		{name: "connection refused", cause: syscall.ECONNREFUSED},
		{name: "connection aborted", cause: syscall.ECONNABORTED},
		{name: "broken pipe", cause: syscall.EPIPE},
		{name: "request timeout", cause: context.DeadlineExceeded},
		{name: "net timeout", cause: timeoutError{}},
		{name: "temporary network error", cause: temporaryError{}},
		{name: "arbitrary URL error", cause: &url.Error{Op: "roundtrip", URL: "http://sidecar.invalid/", Err: errors.New("bounded transport failure")}},
		{name: "503", status: http.StatusServiceUnavailable, wantTransient: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				if test.status != 0 {
					return response(test.status, "text/plain", "busy"), nil
				}
				return nil, test.cause
			})
			httpClient := &http.Client{Transport: transport}
			client := newClient("127.0.0.1:12345", "secret", httpClient, nil, protocol.PickerCD)
			err := client.postLabel(context.Background(), "1/1")
			if test.wantTransient {
				if !errors.Is(err, errTransientCycle) {
					t.Fatalf("postLabel error=%v, want transient cycle", err)
				}
				return
			}
			if !errors.Is(err, errTransport) || errors.Is(err, errTransientCycle) || !errors.Is(err, test.cause) {
				t.Fatalf("postLabel error=%v, want raw typed transport error", err)
			}
		})
	}
}

func TestClientKeepsUnauthorizedAndValidationFailuresTerminal(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "server error", status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(test.status, "text/plain", "failure"), nil
			})
			client := newClient("127.0.0.1:12345", "secret", &http.Client{Transport: transport}, nil, protocol.PickerCD)
			if err := client.postLabel(context.Background(), "1/1"); !errors.Is(err, errInvalidStatus) {
				t.Fatalf("postLabel error=%v, want invalid status", err)
			}
		})
	}
}

func TestClientKeepsParentCancellationTerminal(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client := newClient("127.0.0.1:12345", "secret", &http.Client{Transport: transport}, nil, protocol.PickerCD)
	parent, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.postLabel(parent, "1/1") }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || errors.Is(err, errTransientCycle) {
			t.Fatalf("postLabel error=%v, want parent cancellation without transient classification", err)
		}
	case <-time.After(time.Second):
		t.Fatal("postLabel did not stop after parent cancellation")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "request timed out" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary network failure" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

var _ net.Error = temporaryError{}
