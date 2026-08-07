//go:build windows

package integration

import (
	"encoding/binary"
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

func windowsDescendantPIDs(nodes map[uint32]windowsProcessNode, root uint32) map[uint32]bool {
	descendants := map[uint32]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	delete(descendants, root)
	return descendants
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
			root: {pid: root, ppid: 1},
			202:  {pid: 202, ppid: root},
		},
		{},
	}
	calls := 0
	err := waitForWindowsProcessTreeExit(int(root), time.Now().Add(time.Second), func() (map[uint32]windowsProcessNode, error) {
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
	wantPowerShell, err := filepath.Abs(session.productionRootPath)
	if err != nil {
		t.Fatal(err)
	}
	powershellPIDs := make([]uint32, 0, 1)
	for pid := range windowsDescendantPIDs(nodes, rootPID) {
		node := nodes[pid]
		if node.queryErr == nil && strings.EqualFold(node.exe, wantPowerShell) {
			powershellPIDs = append(powershellPIDs, pid)
		}
	}
	if len(powershellPIDs) != 1 {
		t.Fatalf("PowerShell hosts below bootstrap=%d, want exactly one: %v", len(powershellPIDs), powershellPIDs)
	}
	powershellPID := powershellPIDs[0]
	if nodes[powershellPID].ppid != rootPID {
		t.Fatalf("PowerShell pid %d parent=%d, want bootstrap %d", powershellPID, nodes[powershellPID].ppid, rootPID)
	}
	for pid := range windowsDescendantPIDs(nodes, powershellPID) {
		node := nodes[pid]
		if node.queryErr != nil {
			continue
		}
		switch strings.ToLower(filepath.Base(node.exe)) {
		case "cmd.exe", "powershell.exe", "pwsh.exe":
			t.Fatalf("nested interpreter below PowerShell pid %d: pid=%d exe=%q", powershellPID, pid, node.exe)
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
	root := uint32(session.productionRootPID())
	descendants := map[uint32]bool{root: true}
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
		if pid == root {
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
	root := uint32(session.productionRootPID())
	descendants := map[uint32]bool{root: true}
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
	for pid := range descendants {
		if pid == root {
			continue
		}
		node := nodes[pid]
		if strings.EqualFold(node.exe, wantFZF) {
			return node.command
		}
	}
	t.Fatalf("fzf process not found below picker %d", root)
	return ""
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
	records, err := snapshotDescendantProcessRecords(session.productionRootPID())
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
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		return nil, err
	}
	descendants := map[uint32]bool{uint32(root): true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	records := make([]descendantProcessRecord, 0, len(descendants)-1)
	for pid, node := range nodes {
		if pid == uint32(root) || !descendants[pid] || node.queryErr != nil {
			continue
		}
		command, err := queryWindowsProcessCommandLineByPID(pid)
		if err != nil {
			continue
		}
		records = append(records, descendantProcessRecord{
			PID: int(pid), Identity: fmt.Sprintf("%d:%d", pid, node.creationMarker), CommandLine: command,
		})
	}
	sort.Slice(records, func(left, right int) bool { return records[left].PID < records[right].PID })
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
	root := uint32(session.productionRootPID())
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
