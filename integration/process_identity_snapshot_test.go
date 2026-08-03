package integration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type fakeProcessIdentity struct {
	pid    int
	closed bool
}

func (identity *fakeProcessIdentity) PID() int { return identity.pid }

func (*fakeProcessIdentity) WaitGone(context.Context) error { return nil }

func (identity *fakeProcessIdentity) Close() error {
	identity.closed = true
	return nil
}

func TestCaptureOwnedProcessIdentitiesSkipsDisappearedEntries(t *testing.T) {
	identities := map[int]*fakeProcessIdentity{1: {pid: 1}, 2: {pid: 2}}
	captured, err := captureOwnedProcessIdentities(
		[]processIdentityEntry{{pid: 1, marker: "one"}, {pid: 2, marker: "gone"}},
		func(pid int) (ownedProcessIdentity, error) {
			if pid == 2 {
				return nil, fmt.Errorf("pid %d: %w", pid, errProcessIdentityChanged)
			}
			return identities[pid], nil
		},
		func(_ ownedProcessIdentity, _ string) error { return nil },
		func(err error) bool { return errors.Is(err, errProcessIdentityChanged) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int{captured[0].entry.pid}; !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("captured pids=%v, want [1]", got)
	}
}

func TestCaptureOwnedProcessIdentitiesRejectsReplacedPIDBeforeCapture(t *testing.T) {
	replaced := &fakeProcessIdentity{pid: 7}
	captured, err := captureOwnedProcessIdentities(
		[]processIdentityEntry{{pid: 7, marker: "old"}},
		func(int) (ownedProcessIdentity, error) { return replaced, nil },
		func(_ ownedProcessIdentity, marker string) error {
			if marker == "old" {
				return fmt.Errorf("start marker mismatch: %w", errProcessIdentityChanged)
			}
			return nil
		},
		func(err error) bool { return errors.Is(err, errProcessIdentityChanged) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 0 {
		t.Fatalf("replaced pid captured=%+v", captured)
	}
	if !replaced.closed {
		t.Fatal("replaced process identity was not closed")
	}
}

func TestCaptureOwnedProcessIdentitiesReturnsValidIdentity(t *testing.T) {
	valid := &fakeProcessIdentity{pid: 11}
	captured, err := captureOwnedProcessIdentities(
		[]processIdentityEntry{{pid: 11, marker: "valid"}},
		func(int) (ownedProcessIdentity, error) { return valid, nil },
		func(identity ownedProcessIdentity, marker string) error {
			if identity.PID() != 11 || marker != "valid" {
				return errors.New("unexpected identity snapshot")
			}
			return nil
		},
		func(error) bool { return false },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || captured[0].identity != valid {
		t.Fatalf("captured=%+v, want valid identity", captured)
	}
}

var errProcessIdentityChanged = errors.New("process identity changed")

type processIdentityEntry struct {
	pid    int
	marker string
}

type capturedProcessIdentity struct {
	entry    processIdentityEntry
	identity ownedProcessIdentity
}

func captureOwnedProcessIdentities(
	entries []processIdentityEntry,
	open func(int) (ownedProcessIdentity, error),
	verify func(ownedProcessIdentity, string) error,
	isTransient func(error) bool,
) ([]capturedProcessIdentity, error) {
	captured := make([]capturedProcessIdentity, 0, len(entries))
	for _, entry := range entries {
		identity, err := open(entry.pid)
		if err != nil {
			if isTransient(err) {
				continue
			}
			return nil, fmt.Errorf("open process identity %d: %w", entry.pid, err)
		}
		if err := verify(identity, entry.marker); err != nil {
			_ = identity.Close()
			if isTransient(err) {
				continue
			}
			return nil, fmt.Errorf("verify process identity %d: %w", entry.pid, err)
		}
		captured = append(captured, capturedProcessIdentity{entry: entry, identity: identity})
	}
	return captured, nil
}
