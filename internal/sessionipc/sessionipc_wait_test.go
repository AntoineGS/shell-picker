package sessionipc

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"
)

const (
	sessionIPCWaitTimeout  = 5 * time.Second
	sessionIPCCloseTimeout = 2 * time.Second
	sessionIPCProbeTimeout = 2 * time.Second
)

func awaitPreviewRecord(t *testing.T, recorded <-chan PreviewRequest) PreviewRequest {
	t.Helper()
	return awaitSessionIPC(t, recorded, "preview telemetry record")
}

func awaitSessionIPC[T any](t *testing.T, values <-chan T, operation string) T {
	t.Helper()
	observeCtx, cancel := context.WithTimeout(context.Background(), sessionIPCWaitTimeout)
	defer cancel()
	select {
	case value := <-values:
		return value
	case <-observeCtx.Done():
		t.Fatalf("timed out waiting for %s: %v", operation, observeCtx.Err())
		var zero T
		return zero
	}
}

type sessionIPCShutdownArbiter struct {
	mu            sync.Mutex
	releaseOnce   sync.Once
	releaseCh     chan struct{}
	released      bool
	closeReturned bool
	violation     error
}

func newSessionIPCShutdownArbiter() *sessionIPCShutdownArbiter {
	return &sessionIPCShutdownArbiter{releaseCh: make(chan struct{})}
}

func (arbiter *sessionIPCShutdownArbiter) releaseChannel() <-chan struct{} {
	return arbiter.releaseCh
}

func (arbiter *sessionIPCShutdownArbiter) recordCloseReturn() {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	arbiter.closeReturned = true
	if !arbiter.released && arbiter.violation == nil {
		arbiter.violation = errors.New("Server.Close returned before backend release")
	}
}

func (arbiter *sessionIPCShutdownArbiter) releaseBackend() error {
	arbiter.mu.Lock()
	if !arbiter.released {
		if arbiter.closeReturned && arbiter.violation == nil {
			arbiter.violation = errors.New("Server.Close returned before backend release")
		}
		arbiter.released = true
	}
	violation := arbiter.violation
	arbiter.mu.Unlock()
	arbiter.releaseOnce.Do(func() { close(arbiter.releaseCh) })
	return violation
}

func assertDisplayError(t *testing.T, client *Client, want error, operation string) {
	t.Helper()
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), sessionIPCProbeTimeout)
	defer cancelRequest()
	result := make(chan error, 1)
	go func() {
		_, err := client.Display(requestCtx)
		result <- err
	}()
	err := awaitSessionIPC(t, result, operation)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s ended with request context: %v", operation, err)
	}
	if !errors.Is(err, want) {
		t.Fatalf("%s err=%v want=%v", operation, err, want)
	}
}

func assertSessionIPCListenerClosed(t *testing.T, address string) {
	t.Helper()
	endpoint, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse IPC address for listener probe: %v", err)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), sessionIPCProbeTimeout)
	defer cancelProbe()
	connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp4", endpoint.Host)
	if err == nil {
		connection.Close()
		t.Fatal("IPC listener accepted a connection after Server.Close")
	}
	if probeCtx.Err() != nil {
		t.Fatalf("IPC listener shutdown probe timed out: %v", probeCtx.Err())
	}
	var networkErr *net.OpError
	if !errors.As(err, &networkErr) {
		t.Fatalf("IPC listener shutdown probe returned non-transport error: %v", err)
	}
}

func closeSessionIPC(t *testing.T, server *Server, operation string, onReturn func()) {
	t.Helper()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), sessionIPCCloseTimeout)
	defer cancelClose()
	closed := make(chan error, 1)
	go func() {
		err := server.Close(closeCtx)
		if onReturn != nil {
			onReturn()
		}
		closed <- err
	}()
	if err := awaitSessionIPC(t, closed, "Server.Close "+operation); err != nil {
		t.Fatalf("Server.Close %s: %v", operation, err)
	}
}
