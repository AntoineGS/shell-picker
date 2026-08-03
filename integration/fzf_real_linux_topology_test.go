//go:build linux

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func (session *linuxTerminalSession) AssertProcessTopology(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[int]linuxProcessNode)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		ppid, parentErr := linuxParentPID(pid)
		exe, exeErr := os.Readlink("/proc/" + entry.Name() + "/exe")
		marker, markerErr := linuxProcessStartMarker(pid)
		if parentErr != nil || exeErr != nil || markerErr != nil {
			continue
		}
		raw, _ := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		nodes[pid] = linuxProcessNode{pid: pid, ppid: ppid, exe: exe,
			args: strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"), startMarker: marker}
	}
	descendants := map[int]bool{session.PID(): true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	wantFZF, err := filepath.EvalSymlinks(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	var fzfPIDs []int
	for pid := range descendants {
		if pid != session.PID() && nodes[pid].exe == wantFZF {
			node := nodes[pid]
			identity, identityErr := captureLinuxProcessIdentity(&node)
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
			fzfPIDs = append(fzfPIDs, pid)
		}
	}
	if len(fzfPIDs) != 1 {
		t.Fatalf("fzf child count=%d want 1", len(fzfPIDs))
	}
	rawEnvironment, err := os.ReadFile("/proc/" + strconv.Itoa(fzfPIDs[0]) + "/environ")
	if err != nil {
		t.Fatalf("read owned fzf environment for pid %d: %v", fzfPIDs[0], err)
	}
	actualCredentials, err := parseControlledFZFEnvironment(rawEnvironment)
	if err != nil {
		t.Fatalf("validate owned fzf controlled environment for pid %d: %v", fzfPIDs[0], err)
	}
	for pid := range descendants {
		if pid == session.PID() {
			continue
		}
		node := nodes[pid]
		if node.exe == wantFZF && node.ppid != session.PID() {
			t.Fatalf("fzf pid %d parent=%d want picker %d", pid, node.ppid, session.PID())
		}
		base := strings.ToLower(filepath.Base(node.exe))
		if map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "cmd.exe": true, "powershell.exe": true}[base] {
			t.Fatalf("interpreter role in picker tree pid=%d", pid)
		}
		for _, argument := range node.args {
			if argument == "--listen" || strings.HasPrefix(argument, "--listen=") || strings.Contains(argument, "SHELL_PICKER_TOKEN") {
				t.Fatalf("listener or credential name in process argv pid=%d", pid)
			}
			for _, canary := range session.argvCanaries {
				if canary != "" && strings.Contains(argument, canary) {
					t.Fatalf("stale callback credential canary in process argv pid=%d", pid)
				}
			}
			for _, credential := range actualCredentials {
				if strings.Contains(argument, credential) {
					t.Fatalf("actual controlled callback credential in process argv pid=%d", pid)
				}
			}
		}
	}
}

func (session *linuxTerminalSession) AssertNoLiveDescendants(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[int]linuxProcessNode)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		ppid, parentErr := linuxParentPID(pid)
		if parentErr != nil {
			continue
		}
		exe, _ := os.Readlink("/proc/" + entry.Name() + "/exe")
		nodes[pid] = linuxProcessNode{pid: pid, ppid: ppid, exe: exe}
	}
	root := session.PID()
	if _, exists := nodes[root]; !exists {
		return
	}
	descendants := map[int]bool{root: true}
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

func parseControlledFZFEnvironment(raw []byte) ([]string, error) {
	values := make(map[string]string, 2)
	for _, entry := range bytes.Split(raw, []byte{0}) {
		key, value, ok := bytes.Cut(entry, []byte{'='})
		if !ok {
			continue
		}
		name := string(key)
		if name != "SHELL_PICKER_ADDR" && name != "SHELL_PICKER_TOKEN" {
			continue
		}
		if len(value) == 0 {
			return nil, fmt.Errorf("empty %s", name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate %s", name)
		}
		values[name] = string(value)
	}
	address, addressOK := values["SHELL_PICKER_ADDR"]
	token, tokenOK := values["SHELL_PICKER_TOKEN"]
	if !addressOK || !tokenOK {
		return nil, errors.New("missing controlled callback environment")
	}
	return []string{address, token}, nil
}

func TestParseControlledFZFEnvironmentReturnsOnlyActualCredentials(t *testing.T) {
	raw := []byte("PATH=/bin\x00SHELL_PICKER_ADDR=http://127.0.0.1:321\x00KEEP=yes\x00SHELL_PICKER_TOKEN=actual-token\x00")
	got, err := parseControlledFZFEnvironment(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://127.0.0.1:321", "actual-token"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credential count/order mismatch: got %d values want %d", len(got), len(want))
	}
}

func TestParseControlledFZFEnvironmentRejectsMissingOrDuplicateCredentials(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("SHELL_PICKER_ADDR=http://127.0.0.1:1\x00"),
		[]byte("SHELL_PICKER_ADDR=a\x00SHELL_PICKER_ADDR=b\x00SHELL_PICKER_TOKEN=t\x00"),
	} {
		if values, err := parseControlledFZFEnvironment(raw); err == nil || values != nil {
			t.Fatalf("malformed controlled environment accepted with %d values", len(values))
		}
	}
}

func (session *linuxTerminalSession) TrackLiveDescendants(t *testing.T) []trackedProcess {
	t.Helper()
	nodes := snapshotLinuxPickerProcesses(t)
	root := session.PID()
	if _, exists := nodes[root]; !exists {
		t.Fatalf("picker root %d missing while tracking descendants", root)
	}
	descendants := linuxPickerDescendants(nodes, root)
	wantFZF, err := filepath.EvalSymlinks(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	tracked := make([]trackedProcess, 0, len(descendants))
	fzfCount := 0
	for _, node := range descendants {
		identity, err := captureLinuxProcessIdentity(&node)
		if err != nil {
			if isTransientProcessIdentityError(err) {
				continue
			}
			t.Fatalf("open tracked %s process %d: %v", filepath.Base(node.exe), node.pid, err)
		}
		if node.exe == wantFZF {
			fzfCount++
		}
		tracked = append(tracked, trackedProcess{role: filepath.Base(node.exe), identity: identity})
	}
	if fzfCount != 1 {
		t.Fatalf("tracked fzf descendant count=%d want 1", fzfCount)
	}
	return registerTrackedProcesses(t, tracked)
}

func captureLinuxProcessIdentity(node *linuxProcessNode) (ownedProcessIdentity, error) {
	captured, err := captureOwnedProcessIdentities(
		[]processIdentityEntry{{pid: node.pid, marker: node.startMarker}},
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

func (session *linuxTerminalSession) AssertTrackedProcessesGone(t *testing.T, tracked []trackedProcess) {
	t.Helper()
	assertTrackedProcessesGone(t, tracked)
}

func snapshotLinuxPickerProcesses(t *testing.T) map[int]linuxProcessNode {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[int]linuxProcessNode)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		ppid, parentErr := linuxParentPID(pid)
		exe, exeErr := os.Readlink("/proc/" + entry.Name() + "/exe")
		marker, markerErr := linuxProcessStartMarker(pid)
		if parentErr != nil || exeErr != nil || markerErr != nil {
			continue
		}
		nodes[pid] = linuxProcessNode{pid: pid, ppid: ppid, exe: exe, startMarker: marker}
	}
	return nodes
}

func linuxPickerDescendants(nodes map[int]linuxProcessNode, root int) []linuxProcessNode {
	descendants := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	result := make([]linuxProcessNode, 0, len(descendants)-1)
	for pid := range descendants {
		if pid != root {
			result = append(result, nodes[pid])
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].pid < result[j].pid })
	return result
}
