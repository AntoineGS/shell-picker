//go:build windows

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsProductionRoot(t *testing.T) {
	tests := []struct {
		name          string
		bootstrapPID  int
		productionPID int
		want          int
	}{
		{name: "normal launch", bootstrapPID: 101, want: 101},
		{name: "PowerShell bootstrap", bootstrapPID: 101, productionPID: 202, want: 202},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &windowsTerminalSession{pid: test.bootstrapPID, productionPID: test.productionPID}
			if got := session.productionRootPID(); got != test.want {
				t.Fatalf("productionRootPID() = %d, want %d", got, test.want)
			}
			if got := session.PID(); got != test.want {
				t.Fatalf("PID() = %d, want production root %d", got, test.want)
			}
		})
	}
}

func TestWindowsProcessTreeExitWaitRetriesTransientDescendants(t *testing.T) {
	root := uint32(101)
	snapshots := []map[uint32]windowsProcessNode{
		{
			root: {pid: root, ppid: 1, creationMarker: 100},
			202:  {pid: 202, ppid: root, creationMarker: 200},
		},
		{},
	}
	calls := 0
	err := waitForWindowsProcessTreeIdentityExit(windowsProcessIdentityKey{pid: root, marker: 100}, time.Now().Add(time.Second), func() (map[uint32]windowsProcessNode, error) {
		calls++
		return snapshots[min(calls-1, len(snapshots)-1)], nil
	})
	if err != nil {
		t.Fatalf("waitForWindowsProcessTreeExit() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("snapshot calls = %d, want 2", calls)
	}
}

func TestDiscoverWindowsProductionRootBranches(t *testing.T) {
	fakeIdentity := &fakeProcessIdentity{pid: 202}
	baseNodes := map[uint32]windowsProcessNode{
		101: {pid: 101, ppid: 1, exe: `C:\bootstrap.exe`, creationMarker: 1},
		202: {pid: 202, ppid: 101, exe: `C:\pwsh.exe`, creationMarker: 2},
		303: {pid: 303, ppid: 202, exe: `C:\pwsh.exe`, creationMarker: 3},
	}
	newDeps := func(nodes map[uint32]windowsProcessNode, identity ownedProcessIdentity, verify func(ownedProcessIdentity, string) error) windowsProductionDiscoveryDeps {
		return windowsProductionDiscoveryDeps{
			snapshot: func() (map[uint32]windowsProcessNode, error) { return nodes, nil },
			wait:     func(windows.Handle, uint32) (uint32, error) { return uint32(windows.WAIT_TIMEOUT), nil },
			openIdentity: func(int) (ownedProcessIdentity, error) {
				return identity, nil
			},
			verifyIdentity: verify,
			isTransient:    func(error) bool { return true },
		}
	}

	t.Run("direct child wins over arbitrary descendant", func(t *testing.T) {
		root, err := discoverWindowsProductionRoot(context.Background(), 11, 101, `C:\pwsh.exe`, newDeps(baseNodes, fakeIdentity, func(ownedProcessIdentity, string) error { return nil }))
		if err != nil {
			t.Fatal(err)
		}
		if root.pid != 202 || root.marker != 2 || root.identity != fakeIdentity {
			t.Fatalf("root=%+v, want direct child identity", root)
		}
	})

	t.Run("ambiguous direct children fail", func(t *testing.T) {
		nodes := map[uint32]windowsProcessNode{202: {pid: 202, ppid: 101, exe: `C:\pwsh.exe`, creationMarker: 2}, 204: {pid: 204, ppid: 101, exe: `C:\pwsh.exe`, creationMarker: 4}}
		_, err := discoverWindowsProductionRoot(context.Background(), 11, 101, `C:\pwsh.exe`, newDeps(nodes, fakeIdentity, func(ownedProcessIdentity, string) error { return nil }))
		if !errors.Is(err, errWindowsAmbiguousProductionRoot) {
			t.Fatalf("error=%v, want ambiguous root", err)
		}
	})

	t.Run("bootstrap exit returns immediately", func(t *testing.T) {
		deps := newDeps(nil, fakeIdentity, func(ownedProcessIdentity, string) error { return nil })
		deps.wait = func(windows.Handle, uint32) (uint32, error) { return windows.WAIT_OBJECT_0, nil }
		_, err := discoverWindowsProductionRoot(context.Background(), 11, 101, `C:\pwsh.exe`, deps)
		if !errors.Is(err, errWindowsBootstrapExited) {
			t.Fatalf("error=%v, want bootstrap exit", err)
		}
	})

	t.Run("context cancellation returns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := discoverWindowsProductionRoot(ctx, 11, 101, `C:\pwsh.exe`, newDeps(nil, fakeIdentity, func(ownedProcessIdentity, string) error { return nil }))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want context cancellation", err)
		}
	})

	t.Run("context deadline returns", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now())
		defer cancel()
		_, err := discoverWindowsProductionRoot(ctx, 11, 101, `C:\pwsh.exe`, newDeps(nil, fakeIdentity, func(ownedProcessIdentity, string) error { return nil }))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v, want deadline exceeded", err)
		}
	})

	t.Run("identity change retries", func(t *testing.T) {
		calls := 0
		deps := newDeps(baseNodes, fakeIdentity, func(ownedProcessIdentity, string) error {
			calls++
			if calls == 1 {
				return errProcessIdentityChanged
			}
			return nil
		})
		deps.snapshot = func() (map[uint32]windowsProcessNode, error) { return baseNodes, nil }
		root, err := discoverWindowsProductionRoot(context.WithoutCancel(context.Background()), 11, 101, `C:\pwsh.exe`, deps)
		if err != nil || root.pid != 202 || calls != 2 {
			t.Fatalf("root=%+v error=%v verifyCalls=%d, want retry then success", root, err, calls)
		}
	})
}

func TestWindowsProcessTreeIdentityTrackerRetainsObservedDescendant(t *testing.T) {
	snapshots := []map[uint32]windowsProcessNode{
		{
			101: {pid: 101, ppid: 1, creationMarker: 1},
			202: {pid: 202, ppid: 101, creationMarker: 2},
			303: {pid: 303, ppid: 202, creationMarker: 3},
		},
		{
			303: {pid: 303, ppid: 202, creationMarker: 3},
		},
		{},
	}
	calls := 0
	err := waitForWindowsProcessTreeIdentityExit(windowsProcessIdentityKey{pid: 101, marker: 1}, time.Now().Add(time.Second), func() (map[uint32]windowsProcessNode, error) {
		nodes := snapshots[min(calls, len(snapshots)-1)]
		calls++
		return nodes, nil
	})
	if err != nil {
		t.Fatalf("waitForWindowsProcessTreeIdentityExit() = %v", err)
	}
	if calls != 3 {
		t.Fatalf("snapshot calls=%d, want 3", calls)
	}
}

func TestWindowsProcessTreeIdentityTrackerRejectsPIDReuse(t *testing.T) {
	snapshots := []map[uint32]windowsProcessNode{
		{101: {pid: 101, ppid: 1, creationMarker: 1}, 202: {pid: 202, ppid: 101, creationMarker: 2}},
		{101: {pid: 101, ppid: 1, creationMarker: 1}, 202: {pid: 202, ppid: 101, creationMarker: 99}},
		{},
	}
	calls := 0
	err := waitForWindowsProcessTreeIdentityExit(windowsProcessIdentityKey{pid: 101, marker: 1}, time.Now().Add(time.Second), func() (map[uint32]windowsProcessNode, error) {
		nodes := snapshots[min(calls, len(snapshots)-1)]
		calls++
		return nodes, nil
	})
	if err != nil {
		t.Fatalf("waitForWindowsProcessTreeIdentityExit() = %v", err)
	}
	if calls != 3 {
		t.Fatalf("snapshot calls=%d, want 3 after PID reuse", calls)
	}
}

func TestWindowsProcessTreeIdentityTrackerRejectsStalePPIDReuse(t *testing.T) {
	root := windowsProcessIdentityKey{pid: 101, marker: 100}
	parent := windowsProcessIdentityKey{pid: 202, marker: 200}
	staleChild := windowsProcessIdentityKey{pid: 303, marker: 150}
	staleGrandchild := windowsProcessIdentityKey{pid: 404, marker: 250}
	tracker := windowsProcessTreeTracker{root: root}

	first := map[uint32]windowsProcessNode{
		101: {pid: 101, ppid: 1, creationMarker: 100},
		202: {pid: 202, ppid: 101, creationMarker: 200},
	}
	if !tracker.observe(first) {
		t.Fatal("first observation reported no live process")
	}

	second := map[uint32]windowsProcessNode{
		101: {pid: 101, ppid: 1, creationMarker: 100},
		202: {pid: 202, ppid: 101, creationMarker: 200},
		303: {pid: 303, ppid: 202, creationMarker: 150},
		404: {pid: 404, ppid: 303, creationMarker: 250},
	}
	if !tracker.observe(second) {
		t.Fatal("second observation reported no live process")
	}
	if _, ok := tracker.observed[parent]; !ok {
		t.Fatalf("verified parent identity missing from tracker: %+v", tracker.observed)
	}
	if _, ok := tracker.observed[staleChild]; ok {
		t.Fatalf("stale child with reused PPID was accepted: %+v", tracker.observed)
	}
	if _, ok := tracker.observed[staleGrandchild]; ok {
		t.Fatalf("descendant of stale child was accepted: %+v", tracker.observed)
	}
}

func TestWindowsProcessTreeIdentityTrackerUsesSeededObservedDescendants(t *testing.T) {
	calls := 0
	err := waitForWindowsProcessTreeIdentityExitSeeded(
		windowsProcessIdentityKey{pid: 101, marker: 1}, time.Now().Add(time.Second),
		func() (map[uint32]windowsProcessNode, error) {
			calls++
			if calls == 1 {
				return map[uint32]windowsProcessNode{303: {pid: 303, ppid: 202, creationMarker: 3}}, nil
			}
			return map[uint32]windowsProcessNode{}, nil
		},
		[]windowsProcessIdentityKey{{pid: 202, marker: 2}, {pid: 303, marker: 3}},
	)
	if err != nil {
		t.Fatalf("waitForWindowsProcessTreeIdentityExitSeeded() = %v", err)
	}
	if calls != 2 {
		t.Fatalf("snapshot calls=%d, want 2", calls)
	}
}
