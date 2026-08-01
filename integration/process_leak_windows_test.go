//go:build windows

package integration

import (
	"errors"
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
var task20NtQueryObject = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")

const (
	task20SystemExtendedHandleInformation = 64
	task20StatusInfoLengthMismatch        = 0xc0000004
	task20MaxHandleSnapshotSize           = uint32(256 << 20)
	task20ObjectTypeInformation           = 2
)

type task20HandleKind uint8

const (
	task20HandleUnknown task20HandleKind = iota
	task20HandleFile
	task20HandlePipe
	task20HandleSocket
	task20HandleProcess
	task20HandleJob
	task20HandleThread
	task20HandleEvent
	task20HandleTimer
	task20HandleIOCompletion
	task20HandleWaitCompletion
)

type task20ResourceIdentity struct {
	Identity task20HandleIdentity
	Type     string
	Kind     task20HandleKind
}

func (identity task20ResourceIdentity) applicationOwned() bool {
	switch identity.Kind {
	case task20HandleFile, task20HandlePipe, task20HandleSocket, task20HandleProcess, task20HandleJob:
		return true
	default:
		return false
	}
}

type task20UnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type task20ObjectTypeQuery func(windows.Handle, []byte, *uint32) uint32

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
	if current.handles != baseline.handles || !reflect.DeepEqual(current.handleIdentities, baseline.handleIdentities) {
		added, removed := task20HandleIdentityDifference(baseline.handleIdentities, current.handleIdentities)
		return fmt.Sprintf("handles baseline=%d current=%d; %s", baseline.handles, current.handles,
			formatTask20HandleIdentityDifference(added, removed))
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
	for {
		buffer := make([]byte, size)
		var needed uint32
		status, _, _ := task20NtQuerySystemInformation.Call(task20SystemExtendedHandleInformation,
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(size), uintptr(unsafe.Pointer(&needed)))
		if uint32(status) == task20StatusInfoLengthMismatch {
			next, err := task20NextHandleSnapshotSize(size, needed)
			if err != nil {
				return nil, err
			}
			size = next
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
}

func task20NextHandleSnapshotSize(size, needed uint32) (uint32, error) {
	if size > task20MaxHandleSnapshotSize {
		return 0, fmt.Errorf("extended handle response exceeded %d-byte limit", task20MaxHandleSnapshotSize)
	}
	if needed > task20MaxHandleSnapshotSize {
		return 0, fmt.Errorf("extended handle response exceeded %d-byte limit", task20MaxHandleSnapshotSize)
	}
	if size > ^uint32(0)/2 {
		return 0, errors.New("extended handle response size overflowed")
	}
	next := size * 2
	if next > task20MaxHandleSnapshotSize {
		next = task20MaxHandleSnapshotSize
	}
	if needed > next {
		next = needed
	}
	if next <= size {
		return 0, fmt.Errorf("extended handle response exceeded %d-byte limit", task20MaxHandleSnapshotSize)
	}
	return next, nil
}

func task20QueryObjectType(handle windows.Handle) (string, error) {
	return task20QueryObjectTypeWith(handle, func(handle windows.Handle, buffer []byte, needed *uint32) uint32 {
		status, _, _ := task20NtQueryObject.Call(uintptr(handle), task20ObjectTypeInformation,
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(needed)))
		return uint32(status)
	})
}

func task20QueryObjectTypeWith(handle windows.Handle, query task20ObjectTypeQuery) (string, error) {
	size := uint32(256)
	for {
		buffer := make([]byte, size)
		var needed uint32
		status := query(handle, buffer, &needed)
		if status == 0 {
			return task20ParseObjectType(buffer)
		}
		if status != task20StatusInfoLengthMismatch {
			return "", fmt.Errorf("NtQueryObject(ObjectTypeInformation): NTSTATUS %#x", status)
		}
		next, err := task20NextHandleSnapshotSize(size, needed)
		if err != nil {
			return "", fmt.Errorf("grow object type response: %w", err)
		}
		size = next
	}
}

func task20ParseObjectType(buffer []byte) (string, error) {
	headerSize := int(unsafe.Sizeof(task20UnicodeString{}))
	if len(buffer) < headerSize {
		return "", errors.New("short object type response")
	}
	base := unsafe.Pointer(&buffer[0])
	if uintptr(base)%unsafe.Alignof(task20UnicodeString{}) != 0 {
		return "", errors.New("misaligned object type response")
	}
	header := (*task20UnicodeString)(base)
	if header.Length > header.MaximumLength {
		return "", errors.New("object type response length exceeds maximum length")
	}
	if header.Length%2 != 0 || header.MaximumLength%2 != 0 {
		return "", errors.New("object type response has odd UTF-16 byte length")
	}
	if header.Buffer == nil {
		return "", errors.New("object type response has nil string buffer")
	}
	if uintptr(unsafe.Pointer(header.Buffer))%unsafe.Alignof(uint16(0)) != 0 {
		return "", errors.New("misaligned object type string buffer")
	}

	bufferStart := uintptr(base)
	bufferEnd := bufferStart + uintptr(len(buffer))
	if bufferEnd < bufferStart {
		return "", errors.New("object type response buffer address overflowed")
	}
	stringStart := uintptr(unsafe.Pointer(header.Buffer))
	if stringStart < bufferStart || stringStart > bufferEnd {
		return "", errors.New("object type string buffer escaped response")
	}
	stringEnd := stringStart + uintptr(header.Length)
	if stringEnd < stringStart || stringEnd > bufferEnd {
		return "", errors.New("object type string escaped response")
	}
	maximumEnd := stringStart + uintptr(header.MaximumLength)
	if maximumEnd < stringStart || maximumEnd > bufferEnd {
		return "", errors.New("object type string capacity escaped response")
	}

	return windows.UTF16ToString(unsafe.Slice(header.Buffer, int(header.Length/2))), nil
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
	platformDiff := platformResourceDifference(resourceSnapshot{handleIdentities: baseline}, resourceSnapshot{handleIdentities: current})
	if !strings.Contains(platformDiff, "added=[value=0x48 object=0x1800]") ||
		!strings.Contains(platformDiff, "removed=[value=0x40 object=0x1000]") {
		t.Fatalf("platform diff=%q", platformDiff)
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
