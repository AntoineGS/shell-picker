//go:build windows

package integration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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
