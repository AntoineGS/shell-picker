//go:build windows

package integration

import (
	"errors"
	"fmt"
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

type task20HandlePinAPI struct {
	duplicate func(windows.Handle) (windows.Handle, error)
	snapshot  func() ([]task20SystemHandleTableEntry, error)
	classify  func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error)
	close     func(windows.Handle) error
}

type task20PinnedHandle struct {
	original task20HandleIdentity
	handle   windows.Handle
}

func task20CurrentProcessClassifiedHandles() (map[task20HandleIdentity]task20ResourceIdentity, error) {
	entries, err := task20CurrentProcessHandleEntries()
	if err != nil {
		return nil, err
	}
	return task20ClassifyCapturedHandleEntries(entries, task20HandlePinAPI{
		duplicate: task20DuplicateCurrentProcessHandle,
		snapshot:  task20CurrentProcessHandleEntries,
		classify:  task20ClassifyHandle,
		close:     windows.CloseHandle,
	})
}

func task20DuplicateCurrentProcessHandle(handle windows.Handle) (windows.Handle, error) {
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return duplicate, err
	}
	if duplicate == 0 {
		return 0, fmt.Errorf("DuplicateHandle returned a zero handle for %#x", uintptr(handle))
	}
	return duplicate, nil
}

func task20ClassifyCapturedHandleEntries(entries []task20SystemHandleTableEntry, api task20HandlePinAPI) (map[task20HandleIdentity]task20ResourceIdentity, error) {
	if api.duplicate == nil || api.snapshot == nil || api.classify == nil || api.close == nil {
		return nil, errors.New("incomplete Windows handle pinning API")
	}

	pinned := make([]task20PinnedHandle, 0, len(entries))
	for _, entry := range entries {
		identity := task20HandleIdentity{Value: entry.HandleValue, Object: entry.Object}
		if identity.Value == 0 || identity.Object == 0 {
			return task20FailClosedCapturedHandles(pinned,
				fmt.Errorf("invalid captured handle identity value=%#x object=%#x", identity.Value, identity.Object), api.close)
		}
		duplicate, err := api.duplicate(windows.Handle(entry.HandleValue))
		if duplicate != 0 {
			pinned = append(pinned, task20PinnedHandle{original: identity, handle: duplicate})
		}
		if err != nil {
			return task20FailClosedCapturedHandles(pinned, fmt.Errorf("duplicate captured handle %#x object %#x: %w", identity.Value, identity.Object, err), api.close)
		}
		if duplicate == 0 {
			return task20FailClosedCapturedHandles(pinned, fmt.Errorf("duplicate captured handle %#x object %#x returned zero handle", identity.Value, identity.Object), api.close)
		}
	}

	current, err := api.snapshot()
	if err != nil {
		return task20FailClosedCapturedHandles(pinned, fmt.Errorf("query pinned handle identities: %w", err), api.close)
	}
	objects, err := task20PinnedHandleObjects(current, pinned)
	if err != nil {
		return task20FailClosedCapturedHandles(pinned, err, api.close)
	}

	classified := make(map[task20HandleIdentity]task20ResourceIdentity, len(pinned))
	for _, pin := range pinned {
		object := objects[pin.handle]
		if object != pin.original.Object {
			err := fmt.Errorf("handle %#x object mismatch: captured object %#x, pinned duplicate %#x object %#x", pin.original.Value, pin.original.Object, uintptr(pin.handle), object)
			return task20FailClosedCapturedHandles(pinned, err, api.close)
		}
		resource, err := api.classify(pin.handle, pin.original)
		if err != nil {
			return task20FailClosedCapturedHandles(pinned, fmt.Errorf("classify pinned handle %#x for original %#x object %#x: %w", uintptr(pin.handle), pin.original.Value, pin.original.Object, err), api.close)
		}
		if resource.Identity != pin.original {
			return task20FailClosedCapturedHandles(pinned, fmt.Errorf("classifier changed original identity for handle %#x object %#x to value=%#x object=%#x", pin.original.Value, pin.original.Object, resource.Identity.Value, resource.Identity.Object), api.close)
		}
		classified[pin.original] = resource
	}

	if err := task20ClosePinnedHandles(pinned, api.close); err != nil {
		return nil, err
	}
	return classified, nil
}

func task20PinnedHandleObjects(entries []task20SystemHandleTableEntry, pinned []task20PinnedHandle) (map[windows.Handle]uintptr, error) {
	wanted := make(map[windows.Handle]struct{}, len(pinned))
	for _, pin := range pinned {
		wanted[pin.handle] = struct{}{}
	}
	objects := make(map[windows.Handle]uintptr, len(pinned))
	for _, entry := range entries {
		handle := windows.Handle(entry.HandleValue)
		if _, ok := wanted[handle]; !ok {
			continue
		}
		if _, exists := objects[handle]; exists {
			return nil, fmt.Errorf("pinned handle %#x appeared more than once in object snapshot", uintptr(handle))
		}
		if entry.Object == 0 {
			return nil, fmt.Errorf("pinned handle %#x has zero object identity", uintptr(handle))
		}
		objects[handle] = entry.Object
	}
	for _, pin := range pinned {
		if _, ok := objects[pin.handle]; !ok {
			return nil, fmt.Errorf("pinned handle %#x was absent from object snapshot", uintptr(pin.handle))
		}
	}
	return objects, nil
}

func task20FailClosedCapturedHandles(pinned []task20PinnedHandle, cause error, close func(windows.Handle) error) (map[task20HandleIdentity]task20ResourceIdentity, error) {
	if closeErr := task20ClosePinnedHandles(pinned, close); closeErr != nil {
		return nil, errors.Join(cause, closeErr)
	}
	return nil, cause
}

func task20ClosePinnedHandles(pinned []task20PinnedHandle, close func(windows.Handle) error) error {
	var closeErr error
	for index := len(pinned) - 1; index >= 0; index-- {
		pin := pinned[index]
		if err := close(pin.handle); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("close pinned handle %#x for original %#x object %#x: %w", uintptr(pin.handle), pin.original.Value, pin.original.Object, err))
		}
	}
	return closeErr
}
