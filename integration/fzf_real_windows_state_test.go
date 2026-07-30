//go:build windows

package integration

import (
	"context"
	"sync"

	"golang.org/x/sys/windows"
)

type windowsHandleOps struct {
	close func(windows.Handle) error
}

type windowsHandleOwner struct {
	mu      sync.Mutex
	handles []windows.Handle
	once    sync.Once
	err     error
	ops     windowsHandleOps
}

func newWindowsHandleOwner(ops windowsHandleOps) *windowsHandleOwner {
	return &windowsHandleOwner{ops: ops}
}

func (owner *windowsHandleOwner) add(handle windows.Handle) {
	owner.mu.Lock()
	owner.handles = append(owner.handles, handle)
	owner.mu.Unlock()
}

func (owner *windowsHandleOwner) close() error {
	owner.once.Do(func() {
		owner.mu.Lock()
		handles := append([]windows.Handle(nil), owner.handles...)
		owner.handles = nil
		owner.mu.Unlock()
		for _, handle := range handles {
			if handle != 0 {
				owner.err = errorsJoin(owner.err, owner.ops.close(handle))
			}
		}
	})
	return owner.err
}

func errorsJoin(left, right error) error {
	if left != nil {
		return left
	}
	return right
}

type windowsWaitState struct {
	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newWindowsWaitState() *windowsWaitState { return &windowsWaitState{done: make(chan struct{})} }

func (state *windowsWaitState) complete(err error) {
	state.once.Do(func() {
		state.mu.Lock()
		state.err = err
		state.mu.Unlock()
		close(state.done)
	})
}

func (state *windowsWaitState) wait(ctx context.Context) error {
	select {
	case <-state.done:
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
