package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
)

const directFZFResponseLimit = 64 << 10

func TestRealFZFListenProtocol(t *testing.T) {
	fzfPath := requireRealFZF(t)
	for _, test := range []struct {
		name  string
		label string
	}{
		{name: "cd label", label: "2/2"},
		{name: "cp label", label: "2/2 (1)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := startDirectFZF(t, fzfPath)
			server.writeInput(t, "alpha\nbeta\n")
			server.waitReady(t)

			for range 8 {
				status, body, contentType, err := server.request(t, http.MethodGet, "limit=100&offset=0", nil)
				if err != nil {
					t.Fatalf("GET request: %v", err)
				}
				if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") {
					t.Fatalf("GET status/content-type/body=%d/%q/%q", status, contentType, boundedProtocolBody(body))
				}
				var state struct {
					TotalCount int `json:"totalCount"`
					MatchCount int `json:"matchCount"`
				}
				if err := json.Unmarshal(body, &state); err != nil {
					t.Fatalf("GET body is not JSON: %v; body=%q", err, boundedProtocolBody(body))
				}
				if state.TotalCount != 2 || state.MatchCount != 2 {
					t.Fatalf("GET state=%+v, want two candidates", state)
				}

				action := []byte("change-list-label:" + test.label)
				if string(action) != "change-list-label:2/2" && string(action) != "change-list-label:2/2 (1)" {
					t.Fatalf("unexpected action grammar: %q", action)
				}
				status, body, _, err = server.request(t, http.MethodPost, "", action)
				if err != nil {
					t.Fatalf("POST request action=%q: %v", action, err)
				}
				if status != http.StatusOK {
					t.Fatalf("POST action=%q status/body=%d/%q", action, status, boundedProtocolBody(body))
				}
			}
		})
	}
}

func TestListenProtocolFakeBusyResponseIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, strings.Repeat("busy", directFZFResponseLimit))
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, directFZFResponseLimit+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("fake busy status=%d", response.StatusCode)
	}
	if len(body) != directFZFResponseLimit+1 {
		t.Fatalf("fake busy diagnostic read=%d, want bounded probe %d", len(body), directFZFResponseLimit+1)
	}
}

type directFZFServer struct {
	address string
	apiKey  string
	command *exec.Cmd
	input   io.WriteCloser
	stderr  *boundedBuffer
	client  *http.Client
}

func startDirectFZF(t *testing.T, path string) *directFZFServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	apiKey := randomDirectFZFKey(t)
	command := exec.Command(path, "--listen="+address, "--height=10", "--no-clear")
	command.Env = process.SanitizeEnv(os.Environ(), map[string]string{
		"FZF_API_KEY": apiKey,
		"TERM":        "xterm-256color",
	})
	command.Stdout = io.Discard
	command.Stderr = &boundedBuffer{limit: directFZFResponseLimit}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	stderr := command.Stderr.(*boundedBuffer)
	server := &directFZFServer{
		address: address,
		apiKey:  apiKey,
		command: command,
		input:   input,
		stderr:  stderr,
		client: &http.Client{Timeout: time.Second, Transport: &http.Transport{
			Proxy: nil,
			// Reuse the client, but not fzf's fragile idle TCP connections.
			DisableKeepAlives:   true,
			MaxIdleConns:        2,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     5 * time.Second,
		}},
	}
	t.Cleanup(func() {
		_ = server.input.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		if server.stderr.Len() > 0 {
			t.Logf("direct fzf stderr=%q", server.stderr.String())
		}
	})
	return server
}

func (server *directFZFServer) writeInput(t *testing.T, input string) {
	t.Helper()
	if _, err := io.WriteString(server.input, input); err != nil {
		t.Fatalf("write direct fzf input: %v", err)
	}
}

func (server *directFZFServer) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		status, body, _, err := server.request(t, http.MethodGet, "limit=100&offset=0", nil)
		lastErr = err
		if status == http.StatusOK {
			var state struct {
				TotalCount int `json:"totalCount"`
				MatchCount int `json:"matchCount"`
			}
			if json.Unmarshal(body, &state) == nil && state.TotalCount == 2 && state.MatchCount == 2 {
				return
			}
		}
		if server.command.ProcessState != nil && server.command.ProcessState.Exited() {
			t.Fatalf("direct fzf exited before readiness: %v", server.command.ProcessState)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("direct fzf did not become ready at %s; last request error=%v", server.address, lastErr)
}

func (server *directFZFServer) request(t *testing.T, method, query string, body []byte) (int, []byte, string, error) {
	t.Helper()
	url := "http://" + server.address + "/"
	if query != "" {
		url += "?" + query
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", server.apiKey)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := server.client.Do(request)
	if err != nil {
		return 0, nil, "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, directFZFResponseLimit+1))
	if err != nil {
		t.Fatalf("read direct fzf response: %v", err)
	}
	return response.StatusCode, data, response.Header.Get("Content-Type"), nil
}

func randomDirectFZFKey(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return "direct-" + hex.EncodeToString(raw[:])
}

func boundedProtocolBody(body []byte) string {
	if len(body) > directFZFResponseLimit {
		return fmt.Sprintf("<%d bytes>", len(body))
	}
	return string(body)
}

type boundedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if buffer.buffer == nil {
		buffer.buffer = &bytes.Buffer{}
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		_, _ = buffer.buffer.Write(data[:min(len(data), remaining)])
	}
	return len(data), nil
}

func (buffer *boundedBuffer) Len() int {
	if buffer == nil || buffer.buffer == nil {
		return 0
	}
	return buffer.buffer.Len()
}

func (buffer *boundedBuffer) String() string {
	if buffer == nil || buffer.buffer == nil {
		return ""
	}
	return buffer.buffer.String()
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
