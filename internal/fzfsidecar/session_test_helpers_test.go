package fzfsidecar

import (
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type manualTicker struct {
	channel   chan time.Time
	mu        sync.Mutex
	closed    bool
	armed     bool
	ready     chan struct{}
	stopCalls atomic.Int32
}

func newManualTicker() *manualTicker {
	return &manualTicker{channel: make(chan time.Time, 16), ready: make(chan struct{})}
}

func (ticker *manualTicker) C() <-chan time.Time { return ticker.channel }

func (ticker *manualTicker) Reset(time.Duration) bool {
	ticker.mu.Lock()
	defer ticker.mu.Unlock()
	if ticker.closed {
		return false
	}
	if !ticker.armed {
		ticker.armed = true
		close(ticker.ready)
		ticker.ready = make(chan struct{})
	}
	return true
}

func (ticker *manualTicker) Stop() bool {
	ticker.stopCalls.Add(1)
	ticker.mu.Lock()
	if ticker.closed {
		ticker.mu.Unlock()
		return false
	}
	ticker.closed = true
	close(ticker.ready)
	ticker.ready = make(chan struct{})
	ticker.armed = false
	ticker.mu.Unlock()
	return true
}

func (ticker *manualTicker) Tick(value time.Time) {
	for {
		ticker.mu.Lock()
		if ticker.closed {
			ticker.mu.Unlock()
			return
		}
		if ticker.armed {
			ticker.armed = false
			ticker.mu.Unlock()
			ticker.channel <- value
			return
		}
		ready := ticker.ready
		ticker.mu.Unlock()
		wait := time.NewTimer(time.Second)
		select {
		case <-ready:
			wait.Stop()
		case <-wait.C:
			panic("timed out waiting for manual ticker to arm")
		}
	}
}

type closedTicker struct {
	channel chan time.Time
	stopped atomic.Int32
}

func newClosedTicker() *closedTicker {
	channel := make(chan time.Time)
	close(channel)
	return &closedTicker{channel: channel}
}

func (ticker *closedTicker) C() <-chan time.Time      { return ticker.channel }
func (ticker *closedTicker) Reset(time.Duration) bool { return true }
func (ticker *closedTicker) Stop() bool {
	ticker.stopped.Add(1)
	return true
}

type observingTicker struct {
	channel   chan time.Time
	stopCalls atomic.Int32
}

func newObservingTicker() *observingTicker               { return &observingTicker{channel: make(chan time.Time)} }
func (ticker *observingTicker) C() <-chan time.Time      { return ticker.channel }
func (ticker *observingTicker) Reset(time.Duration) bool { return true }
func (ticker *observingTicker) Stop() bool {
	ticker.stopCalls.Add(1)
	return true
}

type controlledTimer struct {
	channel     chan time.Time
	resetEvents chan time.Duration
	stopCalls   atomic.Int32
}

func newControlledTimer() *controlledTimer {
	return &controlledTimer{channel: make(chan time.Time, 16), resetEvents: make(chan time.Duration, 8)}
}

func (timer *controlledTimer) C() <-chan time.Time { return timer.channel }
func (timer *controlledTimer) Reset(interval time.Duration) bool {
	for {
		select {
		case <-timer.channel:
		default:
			timer.resetEvents <- interval
			return true
		}
	}
}
func (timer *controlledTimer) Stop() bool {
	timer.stopCalls.Add(1)
	return true
}
func (timer *controlledTimer) Fire() { timer.channel <- time.Time{} }

type slowCycleTransport struct {
	getCalls      atomic.Int32
	postCalls     atomic.Int32
	postStarted   chan struct{}
	releasePost   chan struct{}
	secondGET     chan struct{}
	secondGETOnce sync.Once
}

func newSlowCycleTransport() *slowCycleTransport {
	return &slowCycleTransport{postStarted: make(chan struct{}), releasePost: make(chan struct{}), secondGET: make(chan struct{})}
}

func (transport *slowCycleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		if transport.getCalls.Add(1) == 2 {
			transport.secondGETOnce.Do(func() { close(transport.secondGET) })
		}
		return response(http.StatusOK, "application/json", `{"totalCount":1,"matchCount":1,"selected":[]}`), nil
	}
	if transport.postCalls.Add(1) == 1 {
		close(transport.postStarted)
		<-transport.releasePost
	}
	return response(http.StatusOK, "text/plain", "ok"), nil
}

type gatedTransport struct {
	started   chan struct{}
	startOnce sync.Once
	calls     atomic.Int32
	idleCalls atomic.Int32
}

func newGatedTransport() *gatedTransport { return &gatedTransport{started: make(chan struct{})} }
func (transport *gatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	transport.startOnce.Do(func() { close(transport.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}
func (transport *gatedTransport) CloseIdleConnections() { transport.idleCalls.Add(1) }

type blockingBody struct {
	started chan<- struct{}
	closed  chan struct{}
	once    sync.Once
}

func (body *blockingBody) Read([]byte) (int, error) {
	onceSignal(body.started)
	<-body.closed
	return 0, io.EOF
}
func (body *blockingBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock { return &manualClock{now: now} }
func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}
func (clock *manualClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delta)
	clock.mu.Unlock()
}

type fakeFZFServer struct {
	server   *http.Server
	listener net.Listener
	key      string
	state    func(int) string
	gets     chan int
	posted   chan string

	mu        sync.Mutex
	getCount  int
	postCount int
}

func newFakeFZFServer(t *testing.T, state func(int) string) *fakeFZFServer {
	t.Helper()
	fake := &fakeFZFServer{state: state, gets: make(chan int, 32), posted: make(chan string, 32)}
	fake.server = &http.Server{Handler: http.HandlerFunc(fake.serveHTTP)}
	return fake
}

func newServerSession(t *testing.T, picker protocol.Picker, fake *fakeFZFServer, manual *manualTicker) *Session {
	t.Helper()
	session, err := New(picker, WithTimer(func(time.Duration) timer { return manual }), WithInterval(time.Millisecond), WithReadinessTimeout(time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	listener, err := net.Listen("tcp4", session.Address())
	if err != nil {
		t.Fatalf("listen at reserved address: %v", err)
	}
	fake.listener = listener
	fake.key = session.APIKey()
	go func() { _ = fake.server.Serve(listener) }()
	t.Cleanup(func() {
		session.Stop()
		_ = session.Wait()
		_ = fake.server.Close()
	})
	return session
}

func (fake *fakeFZFServer) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("X-Api-Key") != fake.key {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch request.Method {
	case http.MethodGet:
		if request.URL.Path != "/" || request.URL.RawQuery != "limit=100&offset=0" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fake.mu.Lock()
		fake.getCount++
		number := fake.getCount
		fake.mu.Unlock()
		fake.gets <- number
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, fake.state(number))
	case http.MethodPost:
		if request.URL.Path != "/" || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.ContentLength != int64(len(body)) {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fake.mu.Lock()
		fake.postCount++
		fake.mu.Unlock()
		fake.posted <- string(body)
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok")
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (fake *fakeFZFServer) counts() (int, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.getCount, fake.postCount
}

func onceSignal(channel chan<- struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func receiveTestValue[T any](t *testing.T, channel <-chan T, name string) T {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-channel:
		if !ok {
			t.Fatalf("%s channel closed", name)
		}
		return value
	case <-timer.C:
		var zero T
		t.Fatalf("timed out waiting for %s", name)
		return zero
	}
}

func waitForTestSignal(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-channel:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", name)
	}
}
