//go:build windows

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsHandleOwnerClosesExactlyOnceUnderConcurrentClose(t *testing.T) {
	var mu sync.Mutex
	closed := make(map[windows.Handle]int)
	owner := newWindowsHandleOwner(windowsHandleOps{close: func(handle windows.Handle) error {
		mu.Lock()
		closed[handle]++
		mu.Unlock()
		return nil
	}})
	for _, handle := range []windows.Handle{1, 2, 3, 4} {
		owner.add(handle)
	}
	var callers sync.WaitGroup
	for range 16 {
		callers.Add(1)
		go func() { defer callers.Done(); _ = owner.close() }()
	}
	callers.Wait()
	for handle, count := range closed {
		if count != 1 {
			t.Errorf("handle %d closed %d times", handle, count)
		}
	}
	if len(closed) != 4 {
		t.Fatalf("closed=%v", closed)
	}
}

func TestWindowsStoredWaitResultIsRepeatableAndConcurrent(t *testing.T) {
	want := errors.New("exit")
	result := newWindowsWaitState()
	result.complete(want)
	var callers sync.WaitGroup
	for range 16 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if err := result.wait(context.Background()); !errors.Is(err, want) {
				t.Errorf("wait=%v", err)
			}
		}()
	}
	callers.Wait()
}
