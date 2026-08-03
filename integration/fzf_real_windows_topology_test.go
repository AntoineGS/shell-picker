//go:build windows

package integration

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
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

func (session *windowsTerminalSession) TraceEvents() []traceEvent {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	return append([]traceEvent(nil), session.events...)
}

func (session *windowsTerminalSession) AssertProcessTopology(t *testing.T) {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(true)
	if err != nil {
		t.Fatal(err)
	}
	descendants := map[uint32]bool{uint32(session.pid): true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	wantFZF, err := filepath.Abs(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	fzfCount := 0
	for pid := range descendants {
		if pid == uint32(session.pid) {
			continue
		}
		node := nodes[pid]
		if node.queryErr != nil {
			// A Toolhelp snapshot can retain a just-exited child whose process
			// handle is already invalid. It is not a live identity to validate;
			// stable descendants are still checked below.
			continue
		}
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
			if node.ppid != uint32(session.pid) {
				t.Fatalf("fzf pid %d parent=%d want %d", pid, node.ppid, session.pid)
			}
		}
		switch strings.ToLower(filepath.Base(node.exe)) {
		case "cmd.exe", "powershell.exe", "pwsh.exe", "sh.exe", "bash.exe":
			t.Fatalf("interpreter in picker process tree pid=%d", pid)
		}
		lowerCommand := strings.ToLower(node.command)
		if strings.Contains(lowerCommand, "--listen") || strings.Contains(lowerCommand, "shell_picker_token") ||
			strings.Contains(lowerCommand, "http://127.0.0.1:") {
			t.Fatalf("listener or callback loopback form in process command line pid=%d", pid)
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

func (session *windowsTerminalSession) AssertNoLiveDescendants(t *testing.T) {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		t.Fatal(err)
	}
	root := uint32(session.pid)
	if _, exists := nodes[root]; !exists {
		return
	}
	descendants := map[uint32]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	for pid := range descendants {
		if pid != root {
			t.Fatalf("picker retained live descendant %d after close", pid)
		}
	}
}

func (session *windowsTerminalSession) TrackLiveDescendants(t *testing.T) []trackedProcess {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		t.Fatal(err)
	}
	root := uint32(session.pid)
	if _, exists := nodes[root]; !exists {
		t.Fatalf("picker root %d missing while tracking descendants", root)
	}
	descendants := map[uint32]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	pids := make([]int, 0, len(descendants)-1)
	for pid := range descendants {
		if pid != root {
			pids = append(pids, int(pid))
		}
	}
	sort.Ints(pids)
	tracked := make([]trackedProcess, 0, len(pids))
	wantFZF, err := filepath.Abs(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	fzfCount := 0
	for _, rawPID := range pids {
		pid := uint32(rawPID)
		node := nodes[pid]
		if node.queryErr != nil {
			// Toolhelp can report a short-lived descendant after it has exited
			// but before the snapshot's PID record disappears. It is not an
			// observable live identity to retain; stable descendants below are
			// still held by process handles.
			continue
		}
		identity, identityErr := captureWindowsProcessIdentity(&node)
		if identityErr != nil {
			if isTransientProcessIdentityError(identityErr) {
				continue
			}
			t.Fatalf("capture tracked %s process %d: %v", filepath.Base(node.exe), rawPID, identityErr)
		}
		node.identity = identity
		if strings.EqualFold(node.exe, wantFZF) {
			fzfCount++
		}
		tracked = append(tracked, trackedProcess{role: filepath.Base(node.exe), identity: identity})
	}
	if fzfCount != 1 {
		t.Fatalf("tracked fzf descendant count=%d want 1; descendants=%+v", fzfCount, pids)
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
		handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
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
	buffer := make([]byte, 128<<10)
	var returned uint32
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &returned); err != nil {
		return "", err
	}
	type unicodeString struct {
		Length, MaximumLength uint16
		Buffer                *uint16
	}
	value := (*unicodeString)(unsafe.Pointer(&buffer[0]))
	if value.Buffer == nil || value.Length%2 != 0 {
		return "", fmt.Errorf("invalid process command line")
	}
	return windows.UTF16ToString(unsafe.Slice(value.Buffer, int(value.Length/2))), nil
}
