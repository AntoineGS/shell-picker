//go:build windows

package integration

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessNode struct {
	pid, ppid      uint32
	exe            string
	command        string
	creationMarker uint64
	identity       ownedProcessIdentity
	queryErr       error
}

type windowsProcessIdentityKey struct {
	pid    uint32
	marker uint64
}

type windowsProcessTreeTracker struct {
	root     windowsProcessIdentityKey
	observed map[windowsProcessIdentityKey]struct{}
}

func (tracker *windowsProcessTreeTracker) observe(nodes map[uint32]windowsProcessNode) bool {
	observed, live := traverseWindowsProcessIdentities(tracker.root, nodes, tracker.observed)
	tracker.observed = observed
	return live
}

func snapshotWindowsProcessTreeIdentityKeys(root windowsProcessIdentityKey) ([]windowsProcessIdentityKey, error) {
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		return nil, err
	}
	identities, live := traverseWindowsProcessIdentities(root, nodes, nil)
	if !live {
		return nil, nil
	}
	return sortedWindowsProcessIdentities(identities), nil
}

var (
	errWindowsBootstrapExited         = errors.New("bootstrap process exited before production root discovery")
	errWindowsAmbiguousProductionRoot = errors.New("multiple direct production roots discovered")
)

type windowsProductionRoot struct {
	pid      int
	marker   uint64
	identity ownedProcessIdentity
}

type windowsProductionDiscoveryDeps struct {
	snapshot       func() (map[uint32]windowsProcessNode, error)
	wait           func(windows.Handle, uint32) (uint32, error)
	openIdentity   func(int) (ownedProcessIdentity, error)
	verifyIdentity func(ownedProcessIdentity, string) error
	isTransient    func(error) bool
}

func discoverWindowsProductionRoot(ctx context.Context, bootstrapHandle windows.Handle, rootPID uint32, wantPath string, deps windowsProductionDiscoveryDeps) (windowsProductionRoot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	wantPath, err := filepath.Abs(wantPath)
	if err != nil {
		return windowsProductionRoot{}, err
	}
	if deps.snapshot == nil {
		deps.snapshot = func() (map[uint32]windowsProcessNode, error) { return snapshotWindowsProcesses(false) }
	}
	if deps.wait == nil {
		deps.wait = windows.WaitForSingleObject
	}
	if deps.openIdentity == nil {
		deps.openIdentity = openWindowsProductionProcessIdentity
	}
	if deps.verifyIdentity == nil {
		deps.verifyIdentity = verifyProcessIdentityMarker
	}
	if deps.isTransient == nil {
		deps.isTransient = isTransientProcessIdentityError
	}
	for {
		if err := ctx.Err(); err != nil {
			return windowsProductionRoot{}, err
		}
		waitTimeout := uint32(10)
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return windowsProductionRoot{}, ctx.Err()
			}
			if milliseconds := uint32(remaining / time.Millisecond); milliseconds < waitTimeout {
				waitTimeout = milliseconds
				if waitTimeout == 0 {
					waitTimeout = 1
				}
			}
		}
		status, err := deps.wait(bootstrapHandle, waitTimeout)
		if err != nil {
			return windowsProductionRoot{}, err
		}
		if status == uint32(windows.WAIT_OBJECT_0) {
			return windowsProductionRoot{}, errWindowsBootstrapExited
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			return windowsProductionRoot{}, fmt.Errorf("wait for bootstrap discovery: status %#x", status)
		}
		nodes, err := deps.snapshot()
		if err != nil {
			return windowsProductionRoot{}, err
		}
		candidates := make([]windowsProcessNode, 0, 1)
		for _, node := range nodes {
			if node.ppid == rootPID && node.queryErr == nil && strings.EqualFold(node.exe, wantPath) {
				candidates = append(candidates, node)
			}
		}
		sort.Slice(candidates, func(left, right int) bool {
			if candidates[left].pid != candidates[right].pid {
				return candidates[left].pid < candidates[right].pid
			}
			return candidates[left].creationMarker < candidates[right].creationMarker
		})
		if len(candidates) > 1 {
			return windowsProductionRoot{}, errWindowsAmbiguousProductionRoot
		}
		if len(candidates) == 1 {
			candidate := candidates[0]
			captured, err := captureOwnedProcessIdentities(
				[]processIdentityEntry{{pid: int(candidate.pid), marker: strconv.FormatUint(candidate.creationMarker, 10)}},
				deps.openIdentity, deps.verifyIdentity, deps.isTransient)
			if err != nil {
				return windowsProductionRoot{}, err
			}
			if len(captured) == 1 {
				return windowsProductionRoot{pid: int(candidate.pid), marker: candidate.creationMarker, identity: captured[0].identity}, nil
			}
		}
	}
}

type remoteUnicodeString struct {
	Length, MaximumLength uint16
	Buffer                uintptr
}

type remoteProcessParameters struct {
	Reserved1     [16]byte
	Reserved2     [10]uintptr
	ImagePathName remoteUnicodeString
	CommandLine   remoteUnicodeString
}

func (session *windowsTerminalSession) TraceEvents() []traceEvent {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	return append([]traceEvent(nil), session.events...)
}

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

func (session *windowsTerminalSession) AssertPowerShellBootstrapTopology(t *testing.T) {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(true)
	if err != nil {
		t.Fatal(err)
	}
	rootPID := uint32(session.pid)
	root, ok := nodes[rootPID]
	if !ok || root.queryErr != nil {
		t.Fatalf("bootstrap root %d missing or unqueryable: %+v", rootPID, root)
	}
	wantRoot, err := filepath.Abs(session.bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(root.exe, wantRoot) {
		t.Fatalf("bootstrap root executable=%q, want %q", root.exe, wantRoot)
	}
	if session.rootMarker != 0 && root.creationMarker != session.rootMarker {
		t.Fatalf("bootstrap root creation marker=%d, want %d", root.creationMarker, session.rootMarker)
	}
	rootKey := windowsProcessIdentityKey{pid: rootPID, marker: session.rootMarker}
	identities, live := traverseWindowsProcessIdentities(rootKey, nodes, nil)
	if !live {
		t.Fatalf("bootstrap root %d has no identity-verified descendants", rootPID)
	}
	wantPowerShell, err := filepath.Abs(session.productionRootPath)
	if err != nil {
		t.Fatal(err)
	}
	powershellPIDs := make([]uint32, 0, 1)
	for identity := range identities {
		node := nodes[identity.pid]
		if node.ppid == rootPID && strings.EqualFold(node.exe, wantPowerShell) &&
			(session.productionMarker == 0 || identity.marker == session.productionMarker) {
			powershellPIDs = append(powershellPIDs, identity.pid)
		}
	}
	if len(powershellPIDs) != 1 {
		t.Fatalf("PowerShell hosts below bootstrap=%d, want exactly one: %v", len(powershellPIDs), powershellPIDs)
	}
	powershellPID := powershellPIDs[0]
	if nodes[powershellPID].ppid != rootPID {
		t.Fatalf("PowerShell pid %d parent=%d, want bootstrap %d", powershellPID, nodes[powershellPID].ppid, rootPID)
	}
	productionKey := windowsProcessIdentityKey{pid: powershellPID, marker: nodes[powershellPID].creationMarker}
	productionIdentities, _ := traverseWindowsProcessIdentities(productionKey, nodes, nil)
	for identity := range productionIdentities {
		node := nodes[identity.pid]
		switch strings.ToLower(filepath.Base(node.exe)) {
		case "cmd.exe", "powershell.exe", "pwsh.exe":
			t.Fatalf("nested interpreter below PowerShell pid %d: pid=%d exe=%q", powershellPID, identity.pid, node.exe)
		}
	}
	if session.productionPID != int(powershellPID) {
		t.Fatalf("production recorder root=%d, want PowerShell pid %d", session.productionPID, powershellPID)
	}
	if session.PID() != int(powershellPID) {
		t.Fatalf("session PID=%d, want PowerShell pid %d", session.PID(), powershellPID)
	}
	for _, record := range session.DescendantProcessRecords(t) {
		if record.PID == int(rootPID) {
			t.Fatalf("bootstrap helper pid %d included in production descendant records: %+v", rootPID, record)
		}
	}
}

func (session *windowsTerminalSession) AssertProcessTopology(t *testing.T) {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(true)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := session.processTreeRoot()
	root := rootKey.pid
	if rootNode, ok := nodes[root]; ok && rootNode.queryErr == nil && rootKey.marker != 0 && rootNode.creationMarker != rootKey.marker {
		t.Fatalf("production root %d creation marker=%d, want %d", root, rootNode.creationMarker, rootKey.marker)
	}
	identities, live := traverseWindowsProcessIdentities(rootKey, nodes, nil)
	if !live {
		t.Fatalf("production root %d has no identity-verified descendants", root)
	}
	wantFZF, err := filepath.Abs(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	fzfCount := 0
	for identity := range identities {
		pid := identity.pid
		node := nodes[pid]
		if strings.EqualFold(node.exe, wantFZF) {
			identity, identityErr := captureWindowsProcessIdentity(&node)
			if identityErr != nil {
				if isTransientProcessIdentityError(identityErr) {
					continue
				}
				t.Fatalf("capture fzf process identity for pid %d: %v", pid, identityErr)
			}
			node.identity = identity
			nodes[pid] = node
			t.Cleanup(func() {
				if err := identity.Close(); err != nil {
					t.Errorf("close fzf process identity %d: %v", identity.PID(), err)
				}
			})
			fzfCount++
			if node.ppid != root {
				t.Fatalf("fzf pid %d parent=%d want %d", pid, node.ppid, root)
			}
		}
		switch strings.ToLower(filepath.Base(node.exe)) {
		case "cmd.exe", "powershell.exe", "pwsh.exe", "sh.exe", "bash.exe":
			t.Fatalf("interpreter in picker process tree pid=%d", pid)
		}
		lowerCommand := strings.ToLower(node.command)
		if (!session.sidecar && strings.Contains(lowerCommand, "--listen")) || strings.Contains(lowerCommand, "shell_picker_token") ||
			strings.Contains(lowerCommand, "http://127.0.0.1:") {
			t.Fatalf("listener or callback loopback form in process command line pid=%d", pid)
		}
		if session.sidecar && strings.Contains(lowerCommand, "--listen=") && !strings.Contains(lowerCommand, "--listen=127.0.0.1:") {
			t.Fatalf("sidecar listen address is not numeric IPv4 loopback pid=%d", pid)
		}
		for _, canary := range session.argvCanaries {
			if canary != "" && strings.Contains(node.command, canary) {
				t.Fatalf("stale credential canary in process command line pid=%d", pid)
			}
		}
	}
	if fzfCount != 1 {
		t.Fatalf("fzf descendant count=%d want 1", fzfCount)
	}
}

func (session *windowsTerminalSession) FZFCommandLine(t *testing.T) string {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(true)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := session.processTreeRoot()
	root := rootKey.pid
	if rootNode, ok := nodes[root]; ok && rootNode.queryErr == nil && rootKey.marker != 0 && rootNode.creationMarker != rootKey.marker {
		t.Fatalf("production root %d creation marker=%d, want %d", root, rootNode.creationMarker, rootKey.marker)
	}
	identities, live := traverseWindowsProcessIdentities(rootKey, nodes, nil)
	if !live {
		t.Fatalf("production root %d has no identity-verified descendants", root)
	}
	wantFZF, err := filepath.Abs(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	fzfCommands := make([]string, 0, 1)
	for identity := range identities {
		node := nodes[identity.pid]
		if strings.EqualFold(node.exe, wantFZF) {
			fzfCommands = append(fzfCommands, node.command)
		}
	}
	if len(fzfCommands) != 1 {
		t.Fatalf("fzf process count below picker %d=%d, want 1", root, len(fzfCommands))
	}
	return fzfCommands[0]
}

func (session *windowsTerminalSession) DescendantCommandLines(t *testing.T) []string {
	t.Helper()
	records := session.DescendantProcessRecords(t)
	commands := make([]string, 0, len(records))
	for _, record := range records {
		commands = append(commands, record.CommandLine)
	}
	return commands
}

func (session *windowsTerminalSession) DescendantProcessRecords(t *testing.T) []descendantProcessRecord {
	t.Helper()
	if session.recorder != nil {
		session.recorder.CaptureAndWait()
		return session.recorder.Records()
	}
	records, err := snapshotDescendantProcessRecordsForIdentity(session.processTreeRoot())
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func (session *windowsTerminalSession) stopDescendantRecorder() {
	if session.recorder != nil {
		session.recorder.StopAndJoin()
	}
}

func (session *windowsTerminalSession) captureDescendantSample() {
	if session.recorder != nil {
		session.recorder.Capture()
	}
}

func snapshotDescendantProcessRecords(root int) ([]descendantProcessRecord, error) {
	return snapshotDescendantProcessRecordsForIdentity(windowsProcessIdentityKey{pid: uint32(root)})
}

func snapshotDescendantProcessRecordsForIdentity(rootKey windowsProcessIdentityKey) ([]descendantProcessRecord, error) {
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		return nil, err
	}
	identities, live := traverseWindowsProcessIdentities(rootKey, nodes, nil)
	if !live {
		return nil, nil
	}
	keys := sortedWindowsProcessIdentities(identities)
	records := make([]descendantProcessRecord, 0, len(keys))
	for _, key := range keys {
		node := nodes[key.pid]
		command, err := queryWindowsProcessCommandLineByPID(key.pid)
		if err != nil {
			continue
		}
		records = append(records, descendantProcessRecord{
			PID: int(key.pid), Identity: fmt.Sprintf("%d:%d", key.pid, node.creationMarker), CommandLine: command,
		})
	}
	return records, nil
}

func queryWindowsProcessCommandLineByPID(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	return queryWindowsProcessCommandLine(handle)
}

func (session *windowsTerminalSession) AssertNoLiveDescendants(t *testing.T) {
	t.Helper()
	_ = session.VerifiedProcessExits(t, session.DescendantProcessRecords(t))
}

func (session *windowsTerminalSession) TrackLiveDescendants(t *testing.T) []trackedProcess {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := session.processTreeRoot()
	root := rootKey.pid
	if _, exists := nodes[root]; !exists {
		t.Fatalf("picker root %d missing while tracking descendants", root)
	}
	if rootKey.marker != 0 && nodes[root].queryErr == nil && nodes[root].creationMarker != rootKey.marker {
		t.Fatalf("picker root %d creation marker=%d, want %d", root, nodes[root].creationMarker, rootKey.marker)
	}
	identities, live := traverseWindowsProcessIdentities(rootKey, nodes, nil)
	if !live {
		t.Fatalf("picker root %d has no identity-verified descendants", root)
	}
	keys := sortedWindowsProcessIdentities(identities)
	tracked := make([]trackedProcess, 0, len(keys))
	wantFZF, err := filepath.Abs(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	fzfCount := 0
	for _, processIdentity := range keys {
		rawPID := int(processIdentity.pid)
		node := nodes[processIdentity.pid]
		ownedIdentity, identityErr := captureWindowsProcessIdentity(&node)
		if identityErr != nil {
			if isTransientProcessIdentityError(identityErr) {
				continue
			}
			t.Fatalf("capture tracked %s process %d: %v", filepath.Base(node.exe), rawPID, identityErr)
		}
		node.identity = ownedIdentity
		if strings.EqualFold(node.exe, wantFZF) {
			fzfCount++
		}
		tracked = append(tracked, trackedProcess{role: filepath.Base(node.exe), identity: ownedIdentity})
	}
	if fzfCount != 1 {
		t.Fatalf("tracked fzf descendant count=%d want 1; descendants=%+v", fzfCount, keys)
	}
	return registerTrackedProcesses(t, tracked)
}

func (session *windowsTerminalSession) AssertTrackedProcessesGone(t *testing.T, tracked []trackedProcess) {
	t.Helper()
	assertTrackedProcessesGone(t, tracked)
}

func snapshotWindowsProcesses(withCommandLine bool) (map[uint32]windowsProcessNode, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("Toolhelp process snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	nodes := make(map[uint32]windowsProcessNode)
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		node := windowsProcessNode{pid: entry.ProcessID, ppid: entry.ParentProcessID}
		access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION)
		if withCommandLine {
			access |= windows.PROCESS_VM_READ
		}
		handle, openErr := windows.OpenProcess(access, false, entry.ProcessID)
		if openErr != nil {
			node.queryErr = fmt.Errorf("OpenProcess: %w", openErr)
			nodes[entry.ProcessID] = node
			continue
		}
		exe, queryErr := queryWindowsProcessImage(handle)
		var creationMarker uint64
		if queryErr == nil {
			creationMarker, queryErr = windowsProcessCreationTime(handle)
		}
		command := ""
		if queryErr == nil && withCommandLine {
			command, queryErr = queryWindowsProcessCommandLine(handle)
		}
		_ = windows.CloseHandle(handle)
		node.exe, node.command, node.creationMarker, node.queryErr = exe, command, creationMarker, queryErr
		nodes[entry.ProcessID] = node
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil, fmt.Errorf("Toolhelp process iteration: %w", err)
	}
	return nodes, nil
}

func captureWindowsProcessIdentity(node *windowsProcessNode) (ownedProcessIdentity, error) {
	captured, err := captureOwnedProcessIdentities(
		[]processIdentityEntry{{pid: int(node.pid), marker: strconv.FormatUint(node.creationMarker, 10)}},
		openOwnedProcessIdentity,
		verifyProcessIdentityMarker,
		isTransientProcessIdentityError,
	)
	if err != nil {
		return nil, err
	}
	if len(captured) != 1 {
		return nil, errProcessIdentityChanged
	}
	node.identity = captured[0].identity
	return node.identity, nil
}

func queryWindowsProcessImage(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func queryWindowsProcessCommandLine(handle windows.Handle) (string, error) {
	processBasicInformation := make([]byte, 48)
	var returned uint32
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation,
		unsafe.Pointer(&processBasicInformation[0]), uint32(len(processBasicInformation)), &returned); err != nil {
		return "", err
	}
	return readRemoteProcessCommandLine(handle, processBasicInformation)
}

func readRemoteProcessCommandLine(handle windows.Handle, processBasicInformation []byte) (string, error) {
	pointerSize := int(unsafe.Sizeof(uintptr(0)))
	pebAddress, err := readRemotePointer(processBasicInformation[pointerSize:])
	if err != nil {
		return "", fmt.Errorf("decode process PEB address: %w", err)
	}
	paramsOffset := uintptr(0x20)
	if pointerSize == 4 {
		paramsOffset = 0x10
	}
	peb := make([]byte, int(paramsOffset)+pointerSize)
	if err := readRemoteMemory(handle, pebAddress, peb); err != nil {
		return "", fmt.Errorf("read process PEB: %w", err)
	}
	paramsAddress, err := readRemotePointer(peb[int(paramsOffset):])
	if err != nil {
		return "", fmt.Errorf("decode process parameters address: %w", err)
	}
	params := make([]byte, unsafe.Sizeof(remoteProcessParameters{}))
	if err := readRemoteMemory(handle, paramsAddress, params); err != nil {
		return "", fmt.Errorf("read process parameters: %w", err)
	}
	commandOffset := unsafe.Offsetof(remoteProcessParameters{}.CommandLine)
	commandLine := (*remoteUnicodeString)(unsafe.Pointer(&params[commandOffset]))
	if commandLine.Length%2 != 0 || commandLine.MaximumLength < commandLine.Length {
		return "", fmt.Errorf("invalid process command line")
	}
	if commandLine.Length == 0 {
		return "", nil
	}
	if commandLine.Buffer == 0 {
		return "", fmt.Errorf("invalid process command line")
	}
	raw := make([]byte, commandLine.Length)
	if err := readRemoteMemory(handle, commandLine.Buffer, raw); err != nil {
		return "", fmt.Errorf("read process command line: %w", err)
	}
	command := make([]uint16, len(raw)/2)
	for index := range command {
		command[index] = binary.LittleEndian.Uint16(raw[index*2:])
	}
	return windows.UTF16ToString(command), nil
}

func readRemotePointer(data []byte) (uintptr, error) {
	size := int(unsafe.Sizeof(uintptr(0)))
	if len(data) < size {
		return 0, fmt.Errorf("short pointer buffer: %d/%d", len(data), size)
	}
	if size == 8 {
		return uintptr(binary.LittleEndian.Uint64(data[:size])), nil
	}
	return uintptr(binary.LittleEndian.Uint32(data[:size])), nil
}

func readRemoteMemory(handle windows.Handle, address uintptr, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	var read uintptr
	if err := windows.ReadProcessMemory(handle, address, &buffer[0], uintptr(len(buffer)), &read); err != nil {
		return err
	}
	if read != uintptr(len(buffer)) {
		return fmt.Errorf("short read: %d/%d", read, len(buffer))
	}
	return nil
}
