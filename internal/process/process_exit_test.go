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
		{"registration-errno", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: int64(syscall.EINVAL)}}}, waitOK, true},
		{"registration-wrong-process", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid + 1, Process: true, Error: true}}}, waitOK, true},
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
