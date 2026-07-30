//go:build !windows

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

type resourceSnapshot struct {
	descriptors map[string]struct{}
	goroutines  int
	artifacts   map[string]artifactFingerprint
}

func snapshotResources(t *testing.T, roots ...string) resourceSnapshot {
	t.Helper()
	root := "/proc/self/fd"
	if runtime.GOOS != "linux" {
		root = "/dev/fd"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read descriptor directory: %v", err)
	}
	descriptors := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(root, entry.Name()))
		if err != nil {
			continue // The descriptor used by ReadDir may close before inspection.
		}
		descriptors[fmt.Sprintf("%s=%s", entry.Name(), target)] = struct{}{}
	}
	return resourceSnapshot{descriptors: descriptors, goroutines: runtime.NumGoroutine(), artifacts: snapshotArtifacts(t, roots)}
}

func platformResourceDifference(baseline, current resourceSnapshot) string {
	for descriptor := range current.descriptors {
		if _, existed := baseline.descriptors[descriptor]; !existed {
			return "new descriptor " + descriptor
		}
	}
	for descriptor := range baseline.descriptors {
		if _, remains := current.descriptors[descriptor]; !remains {
			return "baseline descriptor changed " + descriptor
		}
	}
	return ""
}

func artifactIdentity(_ string, info os.FileInfo) (uint64, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("artifact has no Unix stat identity")
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
