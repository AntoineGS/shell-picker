package sessionipc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

const (
	maxRequestBody = 64 << 10
	maxHandlers    = 16
)

type Server struct {
	address string
	token   Token
	backend Backend

	listener   net.Listener
	httpServer *http.Server
	baseCancel context.CancelFunc
	serveDone  chan struct{}
	semaphore  chan struct{}
	handlers   sync.WaitGroup
	gate       sync.Mutex
	closing    atomic.Bool
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
}

func Listen(ctx context.Context, token Token, backend Backend) (*Server, error) {
	if backend == nil {
		return nil, errors.New("listen IPC: nil backend")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("listen IPC")
	}
	listener := strictListener{Listener: baseListener}
	port := listener.Addr().(*net.TCPAddr).Port
	baseCtx, baseCancel := context.WithCancel(ctx)
	server := &Server{
		address:    fmt.Sprintf("http://127.0.0.1:%d", port),
		token:      token,
		backend:    backend,
		listener:   listener,
		baseCancel: baseCancel,
		serveDone:  make(chan struct{}),
		semaphore:  make(chan struct{}, maxHandlers),
		closeDone:  make(chan struct{}),
	}
	server.httpServer = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    8 << 10,
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
		ConnContext: withRawAuthorization,
	}
	server.httpServer.SetKeepAlivesEnabled(false)
	go func() {
		defer close(server.serveDone)
		_ = server.httpServer.Serve(listener)
	}()
	return server, nil
}

func (server *Server) Address() string {
	return server.address
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	route, ok := canonicalRoute(request)
	if !ok {
		writeError(response, http.StatusNotFound)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed)
		return
	}
	if !rawAuthorizationValid(request.Context()) || !server.token.authorized(request.Header["Authorization"]) {
		writeError(response, http.StatusUnauthorized)
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType)
		return
	}
	if !server.admit() {
		if server.closing.Load() {
			writeError(response, http.StatusServiceUnavailable)
		} else {
			writeError(response, http.StatusTooManyRequests)
		}
		return
	}
	defer server.release()

	body, err := readRequestBody(response, request)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge)
		} else {
			writeError(response, http.StatusBadRequest)
		}
		return
	}
	switch route {
	case "/v1/event":
		server.handleEvent(response, request, body)
	case "/v1/load":
		server.handleLoad(response, request, body)
	case "/v1/preview":
		server.handlePreview(response, request, body)
	}
}

func (server *Server) admit() bool {
	server.gate.Lock()
	defer server.gate.Unlock()
	if server.closing.Load() {
		return false
	}
	select {
	case server.semaphore <- struct{}{}:
		server.handlers.Add(1)
		return true
	default:
		return false
	}
}

func (server *Server) release() {
	<-server.semaphore
	server.handlers.Done()
}

func (server *Server) handleEvent(response http.ResponseWriter, request *http.Request, body []byte) {
	var input EventRequest
	if decodeObject(body, &input) != nil {
		writeError(response, http.StatusBadRequest)
		return
	}
	query, err := decodeBytes(input.QueryBase64)
	if err != nil {
		writeError(response, http.StatusBadRequest)
		return
	}
	current, err := decodeBytes(input.CurrentItemBase64)
	if err != nil {
		writeError(response, http.StatusBadRequest)
		return
	}
	effect, err := server.backend.HandleEvent(request.Context(), protocol.Event{
		Opcode: input.Opcode, Key: input.Key, Query: query, CurrentItem: current,
	})
	if err != nil {
		writeBackendError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, EventResponse{Effect: effect})
}

func (server *Server) handleLoad(response http.ResponseWriter, request *http.Request, body []byte) {
	var input LoadRequest
	if decodeObject(body, &input) != nil || input.Generation == 0 {
		writeError(response, http.StatusBadRequest)
		return
	}
	data, err := server.backend.LoadGeneration(request.Context(), input.Generation)
	if err != nil {
		writeBackendError(response, err)
		return
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(data)
}

func (server *Server) handlePreview(response http.ResponseWriter, request *http.Request, body []byte) {
	var input PreviewRequest
	if decodeObject(body, &input) != nil || validatePreview(input) != nil {
		writeError(response, http.StatusBadRequest)
		return
	}
	current, err := decodeBytes(input.CurrentItemBase64)
	if err != nil || len(current) == 0 {
		writeError(response, http.StatusBadRequest)
		return
	}
	candidate, err := server.backend.ResolvePreview(request.Context(), current)
	if err != nil {
		writeBackendError(response, err)
		return
	}
	if candidate.Kind == protocol.KindVirtual || len(candidate.Path) == 0 || !filepath.IsAbs(string(candidate.Path)) {
		writeError(response, http.StatusNotFound)
		return
	}
	if input.Phase == "resolve" {
		writeJSON(response, http.StatusOK, PreviewResponse{
			Kind: candidate.Kind, PathBase64: base64.StdEncoding.EncodeToString(candidate.Path), Size: candidate.Size,
			ModTimeUnixNano: candidate.ModTimeUnixNano, Mode: candidate.Mode,
		})
		return
	}
	if err := server.backend.RecordPreview(request.Context(), input); err != nil {
		writeBackendError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) Close(ctx context.Context) error {
	server.closeOnce.Do(func() {
		server.closeErr = server.close(ctx)
		close(server.closeDone)
	})
	<-server.closeDone
	return server.closeErr
}

func (server *Server) close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	server.gate.Lock()
	server.closing.Store(true)
	server.gate.Unlock()
	server.baseCancel()
	_ = server.listener.Close()

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	shutdownErr := server.httpServer.Shutdown(shutdownCtx)
	joined := make(chan struct{})
	go func() {
		server.handlers.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-shutdownCtx.Done():
		_ = server.httpServer.Close()
		<-joined
	}
	_ = server.httpServer.Close()
	<-server.serveDone
	if shutdownErr != nil && !errors.Is(shutdownErr, net.ErrClosed) && !errors.Is(shutdownErr, context.Canceled) &&
		!errors.Is(shutdownErr, context.DeadlineExceeded) {
		return errors.New("close IPC server")
	}
	return nil
}

func canonicalRoute(request *http.Request) (string, bool) {
	if request.URL.RawQuery != "" || request.URL.RawPath != "" {
		return "", false
	}
	switch request.RequestURI {
	case "/v1/event", "/v1/load", "/v1/preview":
		return request.RequestURI, true
	default:
		return "", false
	}
}

func readRequestBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	defer request.Body.Close()
	return io.ReadAll(http.MaxBytesReader(response, request.Body, maxRequestBody))
}

func writeBackendError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrUnknownRecord), errors.Is(err, session.ErrStaleGeneration):
		writeError(response, http.StatusNotFound)
	case errors.Is(err, session.ErrInvalidEvent), errors.Is(err, session.ErrInvalidNavigation):
		writeError(response, http.StatusBadRequest)
	default:
		writeError(response, http.StatusInternalServerError)
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int) {
	message := http.StatusText(status)
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
