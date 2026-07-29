package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHiddenCallbackClosesIdleTransportOnEveryConstructedClientPath(t *testing.T) {
	for _, test := range []struct {
		name, response string
		status, code   int
	}{
		{"success", `{"effect":{}}`, http.StatusOK, 0},
		{"backend error", `{"error":"failed"}`, http.StatusInternalServerError, 1},
		{"render error", `{"effect":{"search":"invalid"}}`, http.StatusOK, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			address, closed, shutdown := callbackCleanupServer(t, test.status, test.response)
			defer shutdown()
			token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
			t.Setenv("SHELL_PICKER_ADDR", address)
			t.Setenv("SHELL_PICKER_TOKEN", token)
			t.Setenv("FZF_KEY", "i")
			t.Setenv("FZF_QUERY", "")
			t.Setenv("FZF_CURRENT_ITEM", "")
			var stdout, stderr bytes.Buffer
			code := callbackMain(context.Background(), []string{"--fzf-shell", "e:mi"}, Streams{Out: &stdout, Err: &stderr})
			if code != test.code || bytes.Contains(stderr.Bytes(), []byte(token)) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("callback client left idle transport open")
			}
		})
	}
}

func callbackCleanupServer(t *testing.T, status int, body string) (string, <-chan struct{}, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	var closeOnce sync.Once
	server := &http.Server{
		Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(status)
			_, _ = response.Write([]byte(body))
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				closeOnce.Do(func() { close(closed) })
			}
		},
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve(listener)
	}()
	return "http://" + listener.Addr().String(), closed, func() {
		_ = server.Close()
		<-serveDone
	}
}
