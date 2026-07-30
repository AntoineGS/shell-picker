//go:build windows

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

type previewController struct {
	t           *testing.T
	name, nonce string
	security    *windows.SecurityAttributes
	mu          sync.Mutex
	events      []controlEvent
	changed     chan struct{}
	clients     map[int]*os.File
	listening   windows.Handle
	closing     bool
	ready       chan struct{}
	closed      chan struct{}
	readers     sync.WaitGroup
}

func newPreviewController(t *testing.T) *previewController {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		t.Fatalf("controller security descriptor: %v", err)
	}
	c := &previewController{t: t, name: `\\.\pipe\shell-picker-controller-` + hex.EncodeToString(raw[:16]),
		nonce: hex.EncodeToString(raw[16:]), security: security, changed: make(chan struct{}), clients: make(map[int]*os.File),
		ready: make(chan struct{}), closed: make(chan struct{})}
	go c.accept()
	select {
	case <-c.ready:
	case <-testContext(t).Done():
		t.Fatal("controller server did not become ready")
	}
	t.Cleanup(c.close)
	return c
}

func (c *previewController) address() string { return c.name }

func (c *previewController) accept() {
	defer close(c.closed)
	first := true
	signalReady := c.ready
	for {
		wide, err := windows.UTF16PtrFromString(c.name)
		if err != nil {
			c.fail(err)
			return
		}
		flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
		if first {
			flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
		}
		handle, err := windows.CreateNamedPipe(wide, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
			windows.PIPE_UNLIMITED_INSTANCES, 4096, 4096, 0, c.security)
		if err != nil {
			c.fail(err)
			return
		}
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			_ = windows.CloseHandle(handle)
			return
		}
		c.listening = handle
		c.mu.Unlock()
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			_ = windows.CloseHandle(handle)
			c.fail(err)
			return
		}
		overlapped := windows.Overlapped{HEvent: event}
		err = windows.ConnectNamedPipe(handle, &overlapped)
		close(signalReady)
		first = false
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			var transferred uint32
			err = windows.GetOverlappedResult(handle, &overlapped, &transferred, true)
		}
		_ = windows.CloseHandle(event)
		c.mu.Lock()
		c.listening = 0
		closing := c.closing
		c.mu.Unlock()
		if closing {
			_ = windows.CloseHandle(handle)
			return
		}
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			_ = windows.CloseHandle(handle)
			c.fail(err)
			return
		}
		var clientPID uint32
		if err := windows.GetNamedPipeClientProcessId(handle, &clientPID); err != nil {
			_ = windows.CloseHandle(handle)
			c.fail(err)
			return
		}
		connection := os.NewFile(uintptr(handle), c.name)
		c.mu.Lock()
		c.clients[int(clientPID)] = connection
		c.mu.Unlock()
		nextReady := make(chan struct{})
		c.readers.Add(1)
		go c.read(connection, int(clientPID), nextReady)
		signalReady = nextReady
	}
}

func (c *previewController) read(connection *os.File, clientPID int, nextReady <-chan struct{}) {
	defer c.readers.Done()
	defer func() {
		c.mu.Lock()
		if c.clients[clientPID] == connection {
			delete(c.clients, clientPID)
		}
		c.mu.Unlock()
		_ = connection.Close()
	}()
	for {
		var event controlEvent
		if err := readControlFrame(connection, &event); err != nil {
			return
		}
		if event.Nonce != c.nonce || event.PID != clientPID {
			c.fail(fmt.Errorf("controller identity mismatch pid=%d event=%+v", clientPID, event))
			return
		}
		if event.Event == "renderer-started" {
			select {
			case <-nextReady:
			case <-c.closed:
				return
			}
			if err := writeControlFrame(connection, controlEvent{Event: "controller-ready", Nonce: c.nonce}); err != nil {
				c.fail(err)
				return
			}
		}
		c.mu.Lock()
		c.events = append(c.events, event)
		close(c.changed)
		c.changed = make(chan struct{})
		c.mu.Unlock()
	}
}

func (c *previewController) fail(err error) {
	c.mu.Lock()
	c.events = append(c.events, controlEvent{Event: "controller-error", PID: -1, Nonce: err.Error()})
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

func (c *previewController) wait(ctx context.Context, event string, count int) controlEvent {
	c.t.Helper()
	for {
		c.mu.Lock()
		seen := 0
		for _, candidate := range c.events {
			if candidate.Event == "controller-error" {
				c.mu.Unlock()
				c.t.Fatalf("controller: %s", candidate.Nonce)
			}
			if candidate.Event == event {
				seen++
				if seen >= count {
					c.mu.Unlock()
					return candidate
				}
			}
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			c.t.Fatalf("wait %s #%d: %v events=%+v", event, count, ctx.Err(), c.snapshot())
		}
	}
}

func (c *previewController) release(pid int) error {
	c.mu.Lock()
	connection := c.clients[pid]
	c.mu.Unlock()
	if connection == nil {
		return errors.New("renderer connection unavailable")
	}
	return writeControlFrame(connection, controlEvent{Event: "release", Nonce: c.nonce})
}

func (c *previewController) snapshot() []controlEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]controlEvent(nil), c.events...)
}

func (c *previewController) close() {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		<-c.closed
		return
	}
	c.closing = true
	listening := c.listening
	for _, client := range c.clients {
		_ = windows.CancelIoEx(windows.Handle(client.Fd()), nil)
	}
	c.mu.Unlock()
	if listening != 0 {
		_ = windows.CancelIoEx(listening, nil)
	}
	<-c.closed
	c.readers.Wait()
}
