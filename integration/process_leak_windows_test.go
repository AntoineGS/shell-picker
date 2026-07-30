//go:build windows

package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsResourceSnapshotUsesExactHandleIdentities(t *testing.T) {
	snapshot := snapshotResources(t)
	if len(snapshot.handleIdentities) == 0 {
		t.Fatal("native current-process handle identity snapshot was empty")
	}
}

func TestWindowsOwnedProcessHandleRegistryReturnsToBaseline(t *testing.T) {
	baseline := snapshotTask20OwnedHandles()
	identity, err := openOwnedProcessIdentity(int(windows.GetCurrentProcessId()))
	if err != nil {
		t.Fatal(err)
	}
	if current := snapshotTask20OwnedHandles(); len(current) != len(baseline)+1 {
		t.Fatalf("owned process handle registry count=%d want=%d", len(current), len(baseline)+1)
	}
	if err := identity.Close(); err != nil {
		t.Fatal(err)
	}
	if current := snapshotTask20OwnedHandles(); !reflect.DeepEqual(current, baseline) {
		t.Fatalf("owned process handle registry did not return to baseline: %v", current)
	}
}

func TestWindowsResourceSnapshotFingerprintsDirectoryReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact-directory")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	before := snapshotArtifacts(t, []string{root})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	after := snapshotArtifacts(t, []string{root})
	if reflect.DeepEqual(before, after) {
		t.Fatal("same-path directory replacement escaped Windows file identity")
	}
}

var task20GetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
var task20NtQuerySystemInformation = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQuerySystemInformation")

const (
	task20SystemExtendedHandleInformation = 64
	task20StatusInfoLengthMismatch        = 0xc0000004
)

type task20SystemHandleTableEntry struct {
	Object                uintptr
	UniqueProcessID       uintptr
	HandleValue           uintptr
	GrantedAccess         uint32
	CreatorBackTraceIndex uint16
	ObjectTypeIndex       uint16
	HandleAttributes      uint32
	Reserved              uint32
}

type task20HandleIdentity struct {
	Value  uintptr
	Object uintptr
}

type resourceSnapshot struct {
	handles          uint32
	handleIdentities map[task20HandleIdentity]struct{}
	ownedHandles     map[windows.Handle]string
	goroutineStacks  map[uint64]string
	artifacts        map[string]artifactFingerprint
}

func snapshotResources(t *testing.T, roots ...string) resourceSnapshot {
	t.Helper()
	var count uint32
	result, _, err := task20GetProcessHandleCount.Call(
		uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	if result == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	identities, err := task20CurrentProcessHandleIdentities()
	if err != nil {
		t.Fatalf("NtQuerySystemInformation(SystemExtendedHandleInformation): %v", err)
	}
	return resourceSnapshot{handles: count, handleIdentities: identities, ownedHandles: snapshotTask20OwnedHandles(),
		goroutineStacks: snapshotGoroutineStacks(), artifacts: snapshotArtifacts(t, roots)}
}

func platformResourceDifference(baseline, current resourceSnapshot) string {
	if current.handles != baseline.handles {
		return fmt.Sprintf("handles baseline=%d current=%d", baseline.handles, current.handles)
	}
	if !reflect.DeepEqual(current.handleIdentities, baseline.handleIdentities) {
		return fmt.Sprintf("exact handle identities baseline=%d current=%d", len(baseline.handleIdentities), len(current.handleIdentities))
	}
	if !reflect.DeepEqual(current.ownedHandles, baseline.ownedHandles) {
		return fmt.Sprintf("Task20 owned handle registry baseline=%v current=%v", baseline.ownedHandles, current.ownedHandles)
	}
	return ""
}

func artifactIdentity(path string, _ os.FileInfo) (uint64, uint64, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	handle, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, 0, err
	}
	defer windows.CloseHandle(handle)
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil {
		return 0, 0, err
	}
	index := uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow)
	return uint64(identity.VolumeSerialNumber), index, nil
}

func task20CurrentProcessHandleIdentities() (map[task20HandleIdentity]struct{}, error) {
	if err := task20NtQuerySystemInformation.Find(); err != nil {
		return nil, err
	}
	size := uint32(1 << 20)
	for attempts := 0; attempts < 8; attempts++ {
		buffer := make([]byte, size)
		var needed uint32
		status, _, _ := task20NtQuerySystemInformation.Call(task20SystemExtendedHandleInformation,
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(size), uintptr(unsafe.Pointer(&needed)))
		if uint32(status) == task20StatusInfoLengthMismatch {
			if needed > size {
				size = needed
			} else {
				size *= 2
			}
			continue
		}
		if status != 0 {
			return nil, fmt.Errorf("NTSTATUS %#x", uint32(status))
		}
		headerSize := 2 * unsafe.Sizeof(uintptr(0))
		if len(buffer) < int(headerSize) {
			return nil, errors.New("short extended handle response")
		}
		count := *(*uintptr)(unsafe.Pointer(&buffer[0]))
		entrySize := unsafe.Sizeof(task20SystemHandleTableEntry{})
		if count > uintptr((len(buffer)-int(headerSize))/int(entrySize)) {
			return nil, errors.New("extended handle response count exceeds buffer")
		}
		pid := uintptr(windows.GetCurrentProcessId())
		entries := make([]task20SystemHandleTableEntry, 0, count)
		for index := uintptr(0); index < count; index++ {
			entry := (*task20SystemHandleTableEntry)(unsafe.Pointer(&buffer[headerSize+index*entrySize]))
			entries = append(entries, *entry)
		}
		return task20HandleIdentitiesForProcess(entries, pid), nil
	}
	return nil, errors.New("extended handle response kept growing")
}

func task20HandleIdentitiesForProcess(entries []task20SystemHandleTableEntry, pid uintptr) map[task20HandleIdentity]struct{} {
	identities := make(map[task20HandleIdentity]struct{})
	for _, entry := range entries {
		if entry.UniqueProcessID == pid {
			identities[task20HandleIdentity{Value: entry.HandleValue, Object: entry.Object}] = struct{}{}
		}
	}
	return identities
}

func TestWindowsHandleIdentityIncludesObjectForReusedSlot(t *testing.T) {
	const pid = 17
	before := task20HandleIdentitiesForProcess([]task20SystemHandleTableEntry{{UniqueProcessID: pid, HandleValue: 0x40, Object: 0x1000}}, pid)
	after := task20HandleIdentitiesForProcess([]task20SystemHandleTableEntry{{UniqueProcessID: pid, HandleValue: 0x40, Object: 0x2000}}, pid)
	if reflect.DeepEqual(before, after) {
		t.Fatal("same numeric handle slot with a different object compared equal")
	}
}

func TestWindowsTask20HandleScopeLifecycleOrdering(t *testing.T) {
	source, err := os.ReadFile("security_resource_test.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	serverStart := strings.Index(text, "func runTask20CancelledPreviewHandler")
	externalStart := strings.Index(text, "func runTask20CancelledExternalPreview")
	if serverStart < 0 || externalStart < 0 {
		t.Fatal("Task20 resource lifecycle helpers are absent")
	}
	server := text[serverStart:externalStart]
	assertTask20SourceOrder(t, server,
		`beginTask20HandleScope(t, "server")`,
		"sessionipc.Listen(",
		"handleScope.Capture(t)",
		"client.ResolvePreview(",
		"client.CloseIdleConnections()",
		"handleScope.RequireClosed(t)")
	external := text[externalStart:]
	assertTask20SourceOrder(t, external,
		"os.Pipe()",
		`beginTask20HandleScope(t, "process/job")`,
		"runner.Run(",
		"awaitTask20ProcessStart(",
		"handleScope.Capture(t)",
		"handleScope.RequireClosed(t)")
}

func assertTask20SourceOrder(t *testing.T, source string, fragments ...string) {
	t.Helper()
	position := -1
	for _, fragment := range fragments {
		next := strings.Index(source, fragment)
		if next <= position {
			t.Fatalf("%q does not follow the prior lifecycle operation", fragment)
		}
		position = next
	}
}
