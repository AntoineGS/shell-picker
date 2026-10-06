//go:build windows

package integration

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
