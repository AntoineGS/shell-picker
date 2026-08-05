package sessionipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func eventRequest() EventRequest {
	return EventRequest{
		Opcode:            protocol.OpEscape,
		Key:               "escape",
		QueryBase64:       base64.StdEncoding.EncodeToString([]byte{0xff, 'q'}),
		CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("file\titem\tL3RtcC94")),
	}
}

type eventResponseErrorWriter struct {
	header http.Header
}

func (writer *eventResponseErrorWriter) Header() http.Header { return writer.header }
func (writer *eventResponseErrorWriter) WriteHeader(int)     {}
func (writer *eventResponseErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("response write failed")
}

func benignBackendForPath(path []byte) backendStub {
	backend := benignBackend()
	backend.resolvePreview = func(_ context.Context, _ []byte) (protocol.ResolvedCandidate, error) {
		return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: append([]byte(nil), path...), Size: 7, Mode: 0o644}, nil
	}
	return backend
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

func TestLoadCarriesExactEventIDAndLoadFinalizationUsesTheSameID(t *testing.T) {
	loaded := make(chan LoadRequest, 1)
	finalized := make(chan LoadFinalizeRequest, 1)
	backend := benignBackend()
	backend.loadGeneration = func(_ context.Context, request LoadRequest) ([]byte, error) {
		loaded <- request
		return []byte("bytes"), nil
	}
	backend.finalizeLoad = func(request LoadFinalizeRequest) { finalized <- request }
	_, client := startServer(t, backend)
	data, err := client.Load(context.Background(), LoadRequest{Generation: 7, EventID: 9})
	if err != nil || string(data) != "bytes" {
		t.Fatalf("Load data=%q err=%v", data, err)
	}
	if got := <-loaded; got != (LoadRequest{Generation: 7, EventID: 9}) {
		t.Fatalf("load request=%+v", got)
	}
	if err := client.FinalizeLoad(context.Background(), LoadFinalizeRequest{EventID: 9, Applied: true}); err != nil {
		t.Fatalf("FinalizeLoad: %v", err)
	}
	if got := <-finalized; got.EventID != 9 || !got.Applied {
		t.Fatalf("load finalization=%+v", got)
	}
}

func TestLoadFinalizeRouteRejectsZeroAndMissingFields(t *testing.T) {
	server, _ := startServer(t, benignBackend())
	for name, body := range map[string]string{
		"zero":    `{"event_id":0,"applied":true}`,
		"missing": `{"event_id":9}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := rawRequest(t, server.Address()+"/v1/load/finalize", fixedToken(7), http.MethodPost, "application/json", []byte(body))
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d want=%d", response.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestCallbackInvocationRouteIsRemoved(t *testing.T) {
	server, _ := startServer(t, benignBackend())
	response := rawRequest(t, server.Address()+"/v1/callback/invocation", fixedToken(7), http.MethodPost,
		"application/json", []byte(`{"kind":"info","outcome":"cd"}`))
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", response.StatusCode, http.StatusNotFound)
	}
}

func TestLoadResponseWriteFailureFinalizesExactRequest(t *testing.T) {
	finalized := make(chan LoadFinalizeRequest, 1)
	backend := benignBackend()
	backend.finalizeLoad = func(request LoadFinalizeRequest) { finalized <- request }
	server := &Server{backend: backend}
	body, err := json.Marshal(LoadRequest{Generation: 7, EventID: 9})
	if err != nil {
		t.Fatal(err)
	}
	server.handleLoad(&eventResponseErrorWriter{header: make(http.Header)}, httptest.NewRequest(http.MethodPost, "/v1/load", bytes.NewReader(body)), body)
	select {
	case got := <-finalized:
		if got.EventID != 9 || got.Applied {
			t.Fatalf("load finalization=%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("load response failure did not finalize exact event")
	}
}
