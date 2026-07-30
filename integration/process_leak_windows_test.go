//go:build windows

package integration

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var task20GetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

type resourceSnapshot struct {
	handles    uint32
	goroutines int
	artifacts  map[string]struct{}
}

func snapshotResources(t *testing.T, roots ...string) resourceSnapshot {
	t.Helper()
	var count uint32
	result, _, err := task20GetProcessHandleCount.Call(
		uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	if result == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	return resourceSnapshot{handles: count, goroutines: runtime.NumGoroutine(), artifacts: snapshotArtifacts(t, roots)}
}

func assertResourcesReturned(t *testing.T, baseline resourceSnapshot, roots ...string) {
	t.Helper()
	current := snapshotResources(t, roots...)
	if current.handles > baseline.handles+2 {
		t.Errorf("process handles=%d baseline=%d after child exit event", current.handles, baseline.handles)
	}
	if current.goroutines > baseline.goroutines+2 {
		t.Errorf("goroutines=%d baseline=%d after all owned completion channels closed", current.goroutines, baseline.goroutines)
	}
	assertArtifactsEqual(t, baseline.artifacts, current.artifacts)
}
