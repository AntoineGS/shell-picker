package fzfsidecar

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSessionAuthenticatedFakeServerFailsSoftOnMalformedUnauthorizedForbiddenAndOversizedState(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		status int
	}{
		{name: "malformed state", body: `{"totalCount":1,"matchCount":1,"selected":"not-an-array"}`, status: http.StatusOK},
		{name: "unauthorized response", body: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden response", body: "forbidden", status: http.StatusForbidden},
		{name: "oversized state", body: strings.Repeat("x", maxResponseBytes+1), status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, err := New(protocol.PickerCP)
			if err != nil {
				t.Fatal(err)
			}

			var requests, posts, authenticationFailures atomic.Int32
			serverKey := session.APIKey()
			if test.status == http.StatusUnauthorized {
				serverKey = "different-server-key"
			}
			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Header.Get("X-Api-Key") != serverKey {
					authenticationFailures.Add(1)
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				if request.Method == http.MethodPost {
					posts.Add(1)
					writer.WriteHeader(http.StatusOK)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			})
			listener, err := net.Listen("tcp4", session.Address())
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: handler}
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(listener) }()
			t.Cleanup(func() {
				session.Stop()
				_ = session.Wait()
				_ = server.Close()
				if serveErr := <-serveDone; serveErr != nil && serveErr != http.ErrServerClosed {
					t.Errorf("fake server: %v", serveErr)
				}
			})

			session.Start(context.Background())
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait() returned poll failure: %v", err)
			}
			if got := posts.Load(); got != 0 {
				t.Fatalf("POST count = %d, want 0 after rejected state", got)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("request count = %d, want one authenticated GET", got)
			}
			if test.status != http.StatusUnauthorized && authenticationFailures.Load() != 0 {
				t.Fatalf("authenticated fake server rejected session key %d times", authenticationFailures.Load())
			}
			if test.status == http.StatusUnauthorized && authenticationFailures.Load() != 1 {
				t.Fatalf("unauthorized request count = %d, want 1", authenticationFailures.Load())
			}
			if requests.Load() > 1 {
				t.Fatalf("session retried terminal state failure: requests=%d", requests.Load())
			}
		})
	}
}
