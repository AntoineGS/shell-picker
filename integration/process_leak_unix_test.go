//go:build !windows

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type resourceSnapshot struct {
	descriptors map[string]struct{}
	goroutines  int
	artifacts   map[string]struct{}
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

func assertResourcesReturned(t *testing.T, baseline resourceSnapshot, roots ...string) {
	t.Helper()
	current := snapshotResources(t, roots...)
	for descriptor := range current.descriptors {
		if _, existed := baseline.descriptors[descriptor]; !existed {
			if runtimeDescriptor(descriptor) {
				continue
			}
			t.Errorf("owned descriptor remained open: %s", descriptor)
		}
	}
	if current.goroutines > baseline.goroutines+2 {
		t.Errorf("goroutines=%d baseline=%d after all owned completion channels closed", current.goroutines, baseline.goroutines)
	}
	assertArtifactsEqual(t, baseline.artifacts, current.artifacts)
}

func runtimeDescriptor(descriptor string) bool {
	return strings.Contains(descriptor, "anon_inode:[eventpoll]") || strings.Contains(descriptor, "anon_inode:[eventfd]")
}
