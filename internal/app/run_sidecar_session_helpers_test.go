package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type concreteSidecarMode uint8

const (
	concreteSidecarBlockedReadiness concreteSidecarMode = iota
	concreteSidecarBlockedGET
	concreteSidecarBlockedPOST
	concreteSidecarReady
)

type concreteSidecarHarness struct {
	mode        concreteSidecarMode
	session     *fzfsidecar.Session
	sidecar     *concreteAppSidecar
	server      *http.Server
	transport   *concreteSidecarTransport
	timer       *concreteSidecarTimer
	started     chan string
	cancelled   chan string
	serverDone  chan struct{}
	callback    *sessionipc.Client
	callbackErr error
	authErrors  atomic.Int32
	active      chan string
	posted      chan string
	key         []byte
	stateCanary string
	observer    *concreteSidecarObserver
}

func newConcreteSidecarFactory(t *testing.T, mode concreteSidecarMode) (fzfSidecarFactory, *concreteSidecarHarness) {
	t.Helper()
	harness := &concreteSidecarHarness{
		mode:       mode,
		started:    make(chan string, 8),
		cancelled:  make(chan string, 8),
		serverDone: make(chan struct{}),
		active:     make(chan string, 8),
		posted:     make(chan string, 8),
	}
	factory := func(picker protocol.Picker) (fzfSidecar, error) {
		harness.timer = newConcreteSidecarTimer()
		harness.transport = newConcreteSidecarTransport(mode, harness.started, harness.cancelled)
		options := []fzfsidecar.Option{
			fzfsidecar.WithTimer(func(time.Duration) fzfsidecar.Timer { return harness.timer }),
			fzfsidecar.WithInterval(time.Millisecond),
			fzfsidecar.WithTransport(harness.transport),
		}
		if harness.observer != nil {
			options = append(options, fzfsidecar.WithObserver(harness.observer))
		}
		if len(harness.key) != 0 {
			options = append(options, fzfsidecar.WithRandomSource(bytes.NewReader(harness.key)))
		}
		session, err := fzfsidecar.New(picker, options...)
		if err != nil {
			return nil, err
		}
		listener, err := net.Listen("tcp4", session.Address())
		if err != nil {
			session.Stop()
			_ = session.Wait()
			return nil, err
		}
		harness.session = session
		harness.server = &http.Server{Handler: harness}
		harness.sidecar = &concreteAppSidecar{session: session, server: harness.server, harness: harness}
		go func() { _ = harness.server.Serve(listener) }()
		return harness.sidecar, nil
	}
	t.Cleanup(func() {
		if harness.sidecar != nil {
			harness.sidecar.Stop()
			_ = harness.sidecar.Wait()
		}
	})
	return factory, harness
}

type concreteSidecarObserver struct {
	mu     sync.Mutex
	events []fzfsidecar.ObserverEvent
}

func (observer *concreteSidecarObserver) Observe(event fzfsidecar.ObserverEvent) {
	observer.mu.Lock()
	observer.events = append(observer.events, event)
	observer.mu.Unlock()
}

func (observer *concreteSidecarObserver) Events() []fzfsidecar.ObserverEvent {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]fzfsidecar.ObserverEvent(nil), observer.events...)
}

func (harness *concreteSidecarHarness) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("X-Api-Key") != harness.session.APIKey() {
		harness.authErrors.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch request.Method {
	case http.MethodGet:
		harness.started <- http.MethodGet
		harness.active <- http.MethodGet
		if harness.mode == concreteSidecarBlockedGET {
			<-request.Context().Done()
			signalConcreteSidecarEvent(harness.cancelled, http.MethodGet)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		state := `{"totalCount":1,"matchCount":1,"selected":[]}`
		if harness.stateCanary != "" {
			state = fmt.Sprintf(`{"totalCount":1,"matchCount":1,"selected":[],"unknown_state_field":%q}`, harness.stateCanary)
		}
		_, _ = io.WriteString(writer, state)
	case http.MethodPost:
		harness.started <- http.MethodPost
		harness.active <- http.MethodPost
		if harness.mode == concreteSidecarBlockedPOST {
			<-request.Context().Done()
			signalConcreteSidecarEvent(harness.cancelled, http.MethodPost)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		harness.posted <- string(body)
		writer.WriteHeader(http.StatusOK)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type concreteAppSidecar struct {
	session  *fzfsidecar.Session
	server   *http.Server
	harness  *concreteSidecarHarness
	waitOnce sync.Once
	waitErr  error
}

func (sidecar *concreteAppSidecar) Address() string { return sidecar.session.Address() }

func (sidecar *concreteAppSidecar) APIKey() string { return sidecar.session.APIKey() }

func (sidecar *concreteAppSidecar) Start(ctx context.Context) { sidecar.session.Start(ctx) }

func (sidecar *concreteAppSidecar) Stop() { sidecar.session.Stop() }

func (sidecar *concreteAppSidecar) Wait() error {
	sidecar.waitOnce.Do(func() {
		sidecar.waitErr = sidecar.session.Wait()
		if sidecar.harness.callback != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, sidecar.harness.callbackErr = sidecar.harness.callback.Load(ctx, sessionipc.LoadRequest{Generation: 1})
			cancel()
		}
		sidecar.harness.transport.closed.Store(true)
		_ = sidecar.server.Close()
		close(sidecar.harness.serverDone)
	})
	return sidecar.waitErr
}

type concreteSidecarTransport struct {
	base      *http.Transport
	mode      concreteSidecarMode
	started   chan<- string
	cancelled chan<- string
	afterWait chan string
	closed    atomic.Bool
	calls     atomic.Int32
}

func newConcreteSidecarTransport(mode concreteSidecarMode, started, cancelled chan<- string) *concreteSidecarTransport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.ForceAttemptHTTP2 = false
	return &concreteSidecarTransport{base: base, mode: mode, started: started, cancelled: cancelled, afterWait: make(chan string, 1)}
}

func (transport *concreteSidecarTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.closed.Load() {
		signalConcreteSidecarEvent(transport.afterWait, request.Method)
		return nil, errors.New("sidecar transport used after Wait")
	}
	transport.calls.Add(1)
	if transport.mode == concreteSidecarBlockedReadiness {
		signalConcreteSidecarEvent(transport.started, request.Method)
		<-request.Context().Done()
		signalConcreteSidecarEvent(transport.cancelled, request.Method)
		return nil, request.Context().Err()
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			signalConcreteSidecarEvent(transport.cancelled, request.Method)
		}
	}
	return response, err
}

func (transport *concreteSidecarTransport) CloseIdleConnections() {
	transport.base.CloseIdleConnections()
}

type concreteSidecarTimer struct {
	mu      sync.Mutex
	channel chan time.Time
	armed   bool
	stopped bool
}

func newConcreteSidecarTimer() *concreteSidecarTimer {
	return &concreteSidecarTimer{channel: make(chan time.Time, 1)}
}

func (timer *concreteSidecarTimer) C() <-chan time.Time { return timer.channel }

func (timer *concreteSidecarTimer) Reset(time.Duration) bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	if timer.stopped {
		return false
	}
	timer.armed = true
	return true
}

func (timer *concreteSidecarTimer) Stop() bool {
	timer.mu.Lock()
	timer.stopped = true
	timer.armed = false
	timer.mu.Unlock()
	return true
}

func (timer *concreteSidecarTimer) Fire() bool {
	timer.mu.Lock()
	if timer.stopped || !timer.armed {
		timer.mu.Unlock()
		return false
	}
	timer.armed = false
	timer.mu.Unlock()
	timer.channel <- time.Time{}
	return true
}

func waitForConcreteRequest(t *testing.T, events <-chan string, method string, harness *concreteSidecarHarness) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-events:
			if got == method {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for sidecar %s request; calls=%d authErrors=%d", method, harness.transport.calls.Load(), harness.authErrors.Load())
		}
	}
}

func assertConcreteSidecarCleanup(t *testing.T, harness *concreteSidecarHarness, callback *sessionipc.Client, callbackMustRemain bool) {
	t.Helper()
	if callback == nil {
		t.Fatal("fzf launch did not create callback client")
	}
	if callbackMustRemain && harness.callbackErr != nil {
		t.Fatalf("callback server was closed before sidecar Wait: %v", harness.callbackErr)
	}
	select {
	case <-harness.serverDone:
	default:
		t.Fatal("sidecar HTTP server was not closed by Wait")
	}
	if got := harness.authErrors.Load(); got != 0 {
		t.Fatalf("unauthenticated sidecar requests = %d", got)
	}
	requestCount := harness.transport.calls.Load()
	if harness.timer.Fire() {
		t.Fatal("poll timer accepted a tick after Wait")
	}
	if got := harness.transport.calls.Load(); got != requestCount {
		t.Fatalf("sidecar request count changed after Wait: %d -> %d", requestCount, got)
	}
	select {
	case method := <-harness.transport.afterWait:
		t.Fatalf("sidecar %s request ran after Wait", method)
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := callback.Load(ctx, sessionipc.LoadRequest{Generation: 1}); err == nil {
		t.Fatal("callback server remained available after RunPicker returned")
	}
}

func signalConcreteSidecarEvent(events chan<- string, method string) {
	select {
	case events <- method:
	default:
	}
}
