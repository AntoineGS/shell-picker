//go:build freebsd

package process

import "syscall"

func nonReapingExitSupported() bool { return true }

func observeProcessExit(pid int) error {
	queue, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(queue)
	change := syscall.Kevent_t{Ident: uint64(pid), Filter: syscall.EVFILT_PROC,
		Flags: syscall.EV_ADD | syscall.EV_ONESHOT | syscall.EV_RECEIPT, Fflags: syscall.NOTE_EXIT}
	events := make([]syscall.Kevent_t, 1)
	return observeKqueueExit(pid, func() exitObserverResult {
		n, err := syscall.Kevent(queue, []syscall.Kevent_t{change}, events, nil)
		return kqueueResult(n, events, err)
	}, func() exitObserverResult {
		n, err := syscall.Kevent(queue, nil, events, nil)
		return kqueueResult(n, events, err)
	})
}

func kqueueResult(n int, events []syscall.Kevent_t, err error) exitObserverResult {
	result := exitObserverResult{N: n, Err: err, Events: make([]exitObserverEvent, len(events))}
	for i, event := range events {
		result.Events[i] = exitObserverEvent{PID: int(event.Ident), Process: event.Filter == syscall.EVFILT_PROC,
			Error: event.Flags&syscall.EV_ERROR != 0, Exit: event.Fflags&syscall.NOTE_EXIT != 0, Data: event.Data}
	}
	return result
}
