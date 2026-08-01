//go:build windows

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsResourceSnapshotUsesExactHandleIdentities(t *testing.T) {
	snapshot := snapshotResources(t)
	if len(snapshot.applicationHandles) == 0 {
		t.Fatal("native current-process application handle snapshot was empty")
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

func TestWindowsApplicationHandleDifferenceIncludesType(t *testing.T) {
	baselineResource := task20ResourceIdentity{Identity: task20HandleIdentity{Value: 0x20, Object: 0x1000}, Type: "Process", Kind: task20HandleProcess}
	currentResource := task20ResourceIdentity{Identity: task20HandleIdentity{Value: 0x20, Object: 0x2000}, Type: "File", Kind: task20HandleFile}
	baseline := map[task20HandleIdentity]task20ResourceIdentity{baselineResource.Identity: baselineResource}
	current := map[task20HandleIdentity]task20ResourceIdentity{currentResource.Identity: currentResource}
	diff := task20ClassifiedHandleDifference(baseline, current)
	want := "added=[kind=File object_type=File value=0x20 object=0x2000] removed=[kind=Process object_type=Process value=0x20 object=0x1000]"
	if diff != want {
		t.Fatalf("difference=%q want=%q", diff, want)
	}
}

var task20GetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

type resourceSnapshot struct {
	handles            uint32
	applicationHandles map[task20HandleIdentity]task20ResourceIdentity
	ownedHandles       map[windows.Handle]string
	goroutineStacks    map[uint64]string
	artifacts          map[string]artifactFingerprint
}

func snapshotResources(t *testing.T, roots ...string) resourceSnapshot {
	t.Helper()
	var count uint32
	result, _, err := task20GetProcessHandleCount.Call(
		uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	if result == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	classified, err := task20CurrentProcessClassifiedHandles()
	if err != nil {
		t.Fatalf("classify current-process handles: %v", err)
	}
	applicationHandles, err := task20ApplicationHandles(classified)
	if err != nil {
		t.Fatalf("filter current-process application handles: %v", err)
	}
	return resourceSnapshot{handles: count, applicationHandles: applicationHandles,
		ownedHandles: snapshotTask20OwnedHandles(), goroutineStacks: snapshotGoroutineStacks(),
		artifacts: snapshotArtifacts(t, roots)}
}

func platformResourceDifference(baseline, current resourceSnapshot) string {
	if !reflect.DeepEqual(current.applicationHandles, baseline.applicationHandles) {
		return fmt.Sprintf("handles baseline=%d current=%d; %s", baseline.handles, current.handles,
			task20ClassifiedHandleDifference(baseline.applicationHandles, current.applicationHandles))
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

func task20HandleIdentityDifference(baseline, current map[task20HandleIdentity]struct{}) (added, removed []task20HandleIdentity) {
	for identity := range current {
		if _, existed := baseline[identity]; !existed {
			added = append(added, identity)
		}
	}
	for identity := range baseline {
		if _, remains := current[identity]; !remains {
			removed = append(removed, identity)
		}
	}
	sort.Slice(added, func(i, j int) bool { return task20HandleIdentityLess(added[i], added[j]) })
	sort.Slice(removed, func(i, j int) bool { return task20HandleIdentityLess(removed[i], removed[j]) })
	return added, removed
}

func task20HandleIdentityLess(left, right task20HandleIdentity) bool {
	if left.Value != right.Value {
		return left.Value < right.Value
	}
	return left.Object < right.Object
}

func formatTask20HandleIdentityDifference(added, removed []task20HandleIdentity) string {
	format := func(identities []task20HandleIdentity) string {
		parts := make([]string, len(identities))
		for index, identity := range identities {
			parts[index] = fmt.Sprintf("value=%#x object=%#x", identity.Value, identity.Object)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprintf("added=%s removed=%s", format(added), format(removed))
}

func TestWindowsHandleIdentityIncludesObjectForReusedSlot(t *testing.T) {
	const pid = 17
	before := task20HandleIdentitiesForProcess([]task20SystemHandleTableEntry{{UniqueProcessID: pid, HandleValue: 0x40, Object: 0x1000}}, pid)
	after := task20HandleIdentitiesForProcess([]task20SystemHandleTableEntry{{UniqueProcessID: pid, HandleValue: 0x40, Object: 0x2000}}, pid)
	if reflect.DeepEqual(before, after) {
		t.Fatal("same numeric handle slot with a different object compared equal")
	}
}

func TestWindowsHandleSnapshotBufferGrowth(t *testing.T) {
	const mib = uint32(1 << 20)
	tests := []struct {
		name         string
		size, needed uint32
		want         uint32
		wantError    bool
	}{
		{name: "reported size", size: mib, needed: 3 * mib, want: 3 * mib},
		{name: "reported size near maximum", size: 130 * mib, needed: 131 * mib, want: 256 * mib},
		{name: "stale reported size", size: 3 * mib, needed: mib, want: 6 * mib},
		{name: "stale growth near maximum", size: 130 * mib, needed: 129 * mib, want: 256 * mib},
		{name: "reported size exceeds maximum", size: 130 * mib, needed: 257 * mib, wantError: true},
		{name: "already at maximum", size: 256 * mib, needed: 0, wantError: true},
		{name: "size overflow", size: ^uint32(0), needed: 0, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := task20NextHandleSnapshotSize(test.size, test.needed)
			if (err != nil) != test.wantError || !test.wantError && got != test.want {
				t.Fatalf("next size=%d err=%v; want size=%d error=%v", got, err, test.want, test.wantError)
			}
		})
	}
}

func TestWindowsHandleSnapshotCanGrowPastFormerRetryCount(t *testing.T) {
	size := uint32(1)
	for range 9 {
		var err error
		size, err = task20NextHandleSnapshotSize(size, 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	if size != 1<<9 {
		t.Fatalf("size after nine growths=%d want=%d", size, 1<<9)
	}
}

func TestWindowsHandleSnapshotGrowthRemainsGeometricWhenNeededKeepsGrowing(t *testing.T) {
	const mib = uint32(1 << 20)
	size := uint32(1)
	for calls := 0; size < 256*mib; calls++ {
		if calls >= 32 {
			t.Fatalf("growth required more than 32 calls at size=%d", size)
		}
		next, err := task20NextHandleSnapshotSize(size, size+1)
		if err != nil {
			t.Fatal(err)
		}
		if next <= size {
			t.Fatalf("growth did not progress: size=%d next=%d", size, next)
		}
		size = next
	}
	if size != 256*mib {
		t.Fatalf("final size=%d want=%d", size, 256*mib)
	}
}

func TestWindowsHandleIdentityDifferenceListsAddedAndRemoved(t *testing.T) {
	baseline := map[task20HandleIdentity]struct{}{
		{Value: 0x40, Object: 0x1000}: {},
		{Value: 0x44, Object: 0x1400}: {},
	}
	current := map[task20HandleIdentity]struct{}{
		{Value: 0x44, Object: 0x1400}: {},
		{Value: 0x48, Object: 0x1800}: {},
	}
	added, removed := task20HandleIdentityDifference(baseline, current)
	if !reflect.DeepEqual(added, []task20HandleIdentity{{Value: 0x48, Object: 0x1800}}) ||
		!reflect.DeepEqual(removed, []task20HandleIdentity{{Value: 0x40, Object: 0x1000}}) {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
	diff := formatTask20HandleIdentityDifference(added, removed)
	if !strings.Contains(diff, "added=[value=0x48 object=0x1800]") ||
		!strings.Contains(diff, "removed=[value=0x40 object=0x1000]") {
		t.Fatalf("diff=%q", diff)
	}
	classifiedBaseline := map[task20HandleIdentity]task20ResourceIdentity{
		{Value: 0x40, Object: 0x1000}: {Identity: task20HandleIdentity{Value: 0x40, Object: 0x1000}, Type: "Process", Kind: task20HandleProcess},
		{Value: 0x44, Object: 0x1400}: {Identity: task20HandleIdentity{Value: 0x44, Object: 0x1400}, Type: "Process", Kind: task20HandleProcess},
	}
	classifiedCurrent := map[task20HandleIdentity]task20ResourceIdentity{
		{Value: 0x44, Object: 0x1400}: {Identity: task20HandleIdentity{Value: 0x44, Object: 0x1400}, Type: "Process", Kind: task20HandleProcess},
		{Value: 0x48, Object: 0x1800}: {Identity: task20HandleIdentity{Value: 0x48, Object: 0x1800}, Type: "Process", Kind: task20HandleProcess},
	}
	platformDiff := platformResourceDifference(resourceSnapshot{applicationHandles: classifiedBaseline}, resourceSnapshot{applicationHandles: classifiedCurrent})
	wantPlatformDiff := "handles baseline=0 current=0; added=[kind=Process object_type=Process value=0x48 object=0x1800] removed=[kind=Process object_type=Process value=0x40 object=0x1000]"
	if platformDiff != wantPlatformDiff {
		t.Fatalf("platform diff=%q want=%q", platformDiff, wantPlatformDiff)
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
