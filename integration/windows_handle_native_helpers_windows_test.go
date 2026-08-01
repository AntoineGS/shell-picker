//go:build windows

package integration

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	task20NtQuerySystemInformation = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQuerySystemInformation")
	task20NtQueryObject            = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
)

const (
	task20SystemExtendedHandleInformation = 64
	task20StatusInfoLengthMismatch        = 0xc0000004
	task20MaxHandleSnapshotSize           = uint32(256 << 20)
	task20MaxHandleSnapshotAttempts       = 3
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

func task20HandleKindName(kind task20HandleKind) string {
	switch kind {
	case task20HandleUnknown:
		return "Unknown"
	case task20HandleFile:
		return "File"
	case task20HandlePipe:
		return "Pipe"
	case task20HandleSocket:
		return "Socket"
	case task20HandleProcess:
		return "Process"
	case task20HandleJob:
		return "Job"
	case task20HandleThread:
		return "Thread"
	case task20HandleEvent:
		return "Event"
	case task20HandleTimer:
		return "Timer"
	case task20HandleIOCompletion:
		return "IoCompletion"
	case task20HandleWaitCompletion:
		return "WaitCompletionPacket"
	default:
		return fmt.Sprintf("Unknown(%d)", uint8(kind))
	}
}

func task20ResourceDiagnostic(resource task20ResourceIdentity) string {
	return fmt.Sprintf("kind=%s object_type=%s value=%#x object=%#x", task20HandleKindName(resource.Kind),
		resource.Type, resource.Identity.Value, resource.Identity.Object)
}

type task20ResourceIdentity struct {
	Identity task20HandleIdentity
	Type     string
	Kind     task20HandleKind
}

func (identity task20ResourceIdentity) applicationOwned() (bool, error) {
	switch identity.Kind {
	case task20HandleFile, task20HandlePipe, task20HandleSocket, task20HandleProcess, task20HandleJob:
		return true, nil
	case task20HandleThread, task20HandleEvent, task20HandleTimer, task20HandleIOCompletion, task20HandleWaitCompletion:
		return false, nil
	default:
		return false, fmt.Errorf("unsupported Windows handle kind %d", identity.Kind)
	}
}

type task20UnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type task20ObjectTypeQuery func(windows.Handle, []byte, *uint32) uint32

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

func task20CurrentProcessHandleIdentities() (map[task20HandleIdentity]struct{}, error) {
	entries, err := task20CurrentProcessHandleEntries()
	if err != nil {
		return nil, err
	}
	return task20HandleIdentitiesForProcess(entries, uintptr(windows.GetCurrentProcessId())), nil
}

func task20CurrentProcessHandleEntries() ([]task20SystemHandleTableEntry, error) {
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
			if entry.UniqueProcessID == pid {
				entries = append(entries, *entry)
			}
		}
		return entries, nil
	}
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

type task20HandleSnapshotAPI struct {
	snapshot func() (map[task20HandleIdentity]struct{}, error)
	classify func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error)
}

func task20CurrentProcessClassifiedHandles() (map[task20HandleIdentity]task20ResourceIdentity, error) {
	return task20ClassifyCoherentHandleSnapshotWith(task20MaxHandleSnapshotAttempts, task20HandleSnapshotAPI{
		snapshot: task20CurrentProcessHandleIdentities,
		classify: task20ClassifyHandle,
	})
}

func task20ClassifyCoherentHandleSnapshotWith(attempts int, api task20HandleSnapshotAPI) (map[task20HandleIdentity]task20ResourceIdentity, error) {
	if attempts <= 0 {
		return nil, errors.New("coherent handle snapshot attempt limit must be positive")
	}
	if api.snapshot == nil || api.classify == nil {
		return nil, errors.New("incomplete coherent handle snapshot API")
	}

	var lastBefore, lastAfter map[task20HandleIdentity]struct{}
	for attempt := 1; attempt <= attempts; attempt++ {
		before, err := api.snapshot()
		if err != nil {
			return nil, fmt.Errorf("capture current-process handle snapshot before classification (attempt %d): %w", attempt, err)
		}
		if err := task20ValidateHandleIdentitySet(before, "before"); err != nil {
			return nil, err
		}

		classified := make(map[task20HandleIdentity]task20ResourceIdentity, len(before))
		for identity := range before {
			resource, err := api.classify(windows.Handle(identity.Value), identity)
			if err != nil {
				return nil, fmt.Errorf("classify handle %#x object %#x: %w", identity.Value, identity.Object, err)
			}
			if resource.Identity != identity {
				return nil, fmt.Errorf("classifier changed original identity for handle %#x object %#x to value=%#x object=%#x", identity.Value, identity.Object, resource.Identity.Value, resource.Identity.Object)
			}
			classified[identity] = resource
		}

		after, err := api.snapshot()
		if err != nil {
			return nil, fmt.Errorf("capture current-process handle snapshot after classification (attempt %d): %w", attempt, err)
		}
		if err := task20ValidateHandleIdentitySet(after, "after"); err != nil {
			return nil, err
		}
		if task20HandleIdentitySetsEqual(before, after) {
			return classified, nil
		}
		lastBefore, lastAfter = before, after
	}

	added, removed := task20HandleIdentityDifference(lastBefore, lastAfter)
	return nil, fmt.Errorf("coherent current-process handle snapshot exhausted after %d attempts: before=%s after=%s; difference=%s", attempts,
		task20FormatHandleIdentitySet(lastBefore), task20FormatHandleIdentitySet(lastAfter), formatTask20HandleIdentityDifference(added, removed))
}

func task20ValidateHandleIdentitySet(set map[task20HandleIdentity]struct{}, phase string) error {
	for identity := range set {
		if identity.Value == 0 || identity.Object == 0 {
			return fmt.Errorf("invalid captured handle identity %s classification: value=%#x object=%#x", phase, identity.Value, identity.Object)
		}
	}
	return nil
}

func task20HandleIdentitySetsEqual(left, right map[task20HandleIdentity]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for identity := range left {
		if _, exists := right[identity]; !exists {
			return false
		}
	}
	return true
}

func task20FormatHandleIdentitySet(set map[task20HandleIdentity]struct{}) string {
	identities := make([]task20HandleIdentity, 0, len(set))
	for identity := range set {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool { return task20HandleIdentityLess(identities[i], identities[j]) })
	parts := make([]string, len(identities))
	for index, identity := range identities {
		parts[index] = fmt.Sprintf("value=%#x object=%#x", identity.Value, identity.Object)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
