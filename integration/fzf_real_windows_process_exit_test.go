//go:build windows

package integration

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func verifiedWindowsProcessExits(records []descendantProcessRecord, nodes map[uint32]windowsProcessNode) ([]string, error) {
	exited := make([]string, 0, len(records))
	for _, record := range records {
		pidText, markerText, ok := strings.Cut(record.Identity, ":")
		pid, pidErr := strconv.ParseUint(pidText, 10, 32)
		marker, markerErr := strconv.ParseUint(markerText, 10, 64)
		if !ok || pidErr != nil || markerErr != nil || record.PID <= 0 || uint64(record.PID) != pid {
			return nil, fmt.Errorf("invalid recorded process identity %q", record.Identity)
		}
		node, exists := nodes[uint32(pid)]
		if !exists {
			exited = append(exited, record.Identity)
			continue
		}
		if node.queryErr != nil {
			return nil, fmt.Errorf("verify recorded process %s: %w", record.Identity, node.queryErr)
		}
		if node.creationMarker == marker {
			return nil, fmt.Errorf("recorded process %s remains live", record.Identity)
		}
		exited = append(exited, record.Identity)
	}
	return exited, nil
}

func (session *windowsTerminalSession) VerifiedProcessExits(t *testing.T, records []descendantProcessRecord) []string {
	t.Helper()
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		t.Fatal(err)
	}
	exited, err := verifiedWindowsProcessExits(records, nodes)
	if err != nil {
		t.Fatal(err)
	}
	return exited
}
