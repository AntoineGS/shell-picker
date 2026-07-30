//go:build windows

package integration

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var task20GetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

type resourceSnapshot struct {
	handles    uint32
	goroutines int
	artifacts  map[string]artifactFingerprint
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

func platformResourceDifference(baseline, current resourceSnapshot) string {
	if current.handles != baseline.handles {
		return fmt.Sprintf("handles baseline=%d current=%d", baseline.handles, current.handles)
	}
	return ""
}

func artifactIdentity(path string, info os.FileInfo) (uint64, uint64, error) {
	if !info.Mode().IsRegular() {
		return 0, 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		return 0, 0, err
	}
	index := uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow)
	return uint64(identity.VolumeSerialNumber), index, nil
}
