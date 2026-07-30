//go:build windows

package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessNode struct {
	pid, ppid uint32
	exe       string
	command   string
	queryErr  error
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
			t.Fatalf("query descendant process %d: %v", pid, node.queryErr)
		}
		if strings.EqualFold(node.exe, wantFZF) {
			fzfCount++
			if node.ppid != uint32(session.pid) {
				t.Fatalf("fzf pid %d parent=%d want %d", pid, node.ppid, session.pid)
			}
		}
		switch strings.ToLower(filepath.Base(node.exe)) {
		case "cmd.exe", "powershell.exe", "pwsh.exe", "sh.exe", "bash.exe":
			t.Fatalf("interpreter in picker process tree: %+v", node)
		}
		lowerCommand := strings.ToLower(node.command)
		if strings.Contains(lowerCommand, "--listen") || strings.Contains(lowerCommand, "shell_picker_token") ||
			strings.Contains(lowerCommand, "http://127.0.0.1:") {
			t.Fatalf("listener or credential name in process command line: %+v", node)
		}
		for _, canary := range session.argvCanaries {
			if canary != "" && strings.Contains(node.command, canary) {
				t.Fatalf("credential canary in process command line: %+v", node)
			}
		}
	}
	if fzfCount != 1 {
		t.Fatalf("fzf descendant count=%d want 1", fzfCount)
	}
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
		command := ""
		if queryErr == nil && withCommandLine {
			command, queryErr = queryWindowsProcessCommandLine(handle)
		}
		_ = windows.CloseHandle(handle)
		node.exe, node.command, node.queryErr = exe, command, queryErr
		nodes[entry.ProcessID] = node
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil, fmt.Errorf("Toolhelp process iteration: %w", err)
	}
	return nodes, nil
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
