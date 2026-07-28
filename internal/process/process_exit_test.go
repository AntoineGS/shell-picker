package process

import (
	"errors"
	"syscall"
	"testing"
)

func TestKqueueEventValidation(t *testing.T) {
	const pid = 42
	registrationOK := exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true}}}
	waitOK := exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Exit: true}}}
	tests := []struct {
		name               string
		registration, wait exitObserverResult
		wantErr            bool
	}{
		{"valid", registrationOK, waitOK, false},
		{"registration-syscall-error", exitObserverResult{Err: syscall.EBADF}, waitOK, true},
		{"registration-zero-count", exitObserverResult{}, waitOK, true},
		{"registration-out-of-range-count", exitObserverResult{N: 2, Events: registrationOK.Events}, waitOK, true},
		{"registration-errno", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: int64(syscall.EINVAL)}}}, waitOK, true},
		{"registration-wrong-process", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid + 1, Process: true, Error: true}}}, waitOK, true},
		{"registration-wrong-filter", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Error: true}}}, waitOK, true},
		{"registration-missing-receipt", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true}}}, waitOK, true},
		{"wait-syscall-error", registrationOK, exitObserverResult{Err: syscall.EINTR}, true},
		{"wait-zero-count", registrationOK, exitObserverResult{}, true},
		{"wait-out-of-range-count", registrationOK, exitObserverResult{N: 2, Events: waitOK.Events}, true},
		{"wait-error-event", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: int64(syscall.ECHILD)}}}, true},
		{"wait-mismatched-pid", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid + 1, Process: true, Exit: true}}}, true},
		{"wait-wrong-filter", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Exit: true}}}, true},
		{"wait-missing-note-exit", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true}}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateKqueueObserverResults(pid, test.registration, test.wait)
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, test.wantErr)
			}
			if test.wantErr && !errors.Is(err, ErrExitObserver) {
				t.Fatalf("error not ErrExitObserver: %v", err)
			}
		})
	}
}

func TestKqueueRegistrationValidatedBeforeWait(t *testing.T) {
	const pid = 42
	registrationOK := exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true}}}
	waitOK := exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Exit: true}}}
	for _, test := range []struct {
		name         string
		registration exitObserverResult
		wantErr      bool
		wantWaits    int
	}{
		{"syscall-error", exitObserverResult{Err: syscall.EBADF}, true, 0},
		{"zero-count", exitObserverResult{}, true, 0},
		{"out-of-range-count", exitObserverResult{N: 2, Events: registrationOK.Events}, true, 0},
		{"errno", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: int64(syscall.EINVAL)}}}, true, 0},
		{"wrong-process", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid + 1, Process: true, Error: true}}}, true, 0},
		{"wrong-filter", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Error: true}}}, true, 0},
		{"missing-receipt", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true}}}, true, 0},
		{"valid", registrationOK, false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			waits := 0
			err := observeKqueueExit(pid, func() exitObserverResult { return test.registration }, func() exitObserverResult {
				waits++
				return waitOK
			})
			if (err != nil) != test.wantErr || waits != test.wantWaits {
				t.Fatalf("err=%v waits=%d", err, waits)
			}
		})
	}
}
