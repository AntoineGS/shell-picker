//go:build windows

package integration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func task20ApplicationHandles(classified map[task20HandleIdentity]task20ResourceIdentity) map[task20HandleIdentity]task20ResourceIdentity {
	application := make(map[task20HandleIdentity]task20ResourceIdentity, len(classified))
	for identity, resource := range classified {
		owned, err := resource.applicationOwned()
		if err != nil || !owned {
			continue
		}
		application[identity] = resource
	}
	return application
}

func task20ClassifiedHandleDifference(baseline, current map[task20HandleIdentity]task20ResourceIdentity) string {
	var added, removed []task20ResourceIdentity
	for identity, resource := range current {
		previous, existed := baseline[identity]
		if existed && previous == resource {
			continue
		}
		resource.Identity = identity
		added = append(added, resource)
	}
	for identity, resource := range baseline {
		latest, remains := current[identity]
		if remains && latest == resource {
			continue
		}
		resource.Identity = identity
		removed = append(removed, resource)
	}
	sort.Slice(added, func(i, j int) bool { return task20ResourceIdentityLess(added[i], added[j]) })
	sort.Slice(removed, func(i, j int) bool { return task20ResourceIdentityLess(removed[i], removed[j]) })

	format := func(resources []task20ResourceIdentity) string {
		parts := make([]string, len(resources))
		for index, resource := range resources {
			parts[index] = fmt.Sprintf("type=%s value=%#x object=%#x", resource.Type,
				resource.Identity.Value, resource.Identity.Object)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprintf("added=%s removed=%s", format(added), format(removed))
}

func task20ResourceIdentityLess(left, right task20ResourceIdentity) bool {
	if !task20HandleIdentityLess(left.Identity, right.Identity) {
		if task20HandleIdentityLess(right.Identity, left.Identity) {
			return false
		}
		return left.Type < right.Type
	}
	return true
}

func task20CurrentProcessClassifiedHandles() (map[task20HandleIdentity]task20ResourceIdentity, error) {
	entries, err := task20CurrentProcessHandleEntries()
	if err != nil {
		return nil, err
	}
	classified := make(map[task20HandleIdentity]task20ResourceIdentity, len(entries))
	for _, entry := range entries {
		identity := task20HandleIdentity{Value: entry.HandleValue, Object: entry.Object}
		resource, err := task20ClassifyHandle(windows.Handle(entry.HandleValue), identity)
		if err != nil {
			return nil, fmt.Errorf("classify handle %#x object %#x: %w", entry.HandleValue, entry.Object, err)
		}
		classified[identity] = resource
	}
	return classified, nil
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

type task20HandleScope struct {
	kind     string
	baseline map[task20HandleIdentity]struct{}
	owned    []task20HandleIdentity
}

func beginTask20HandleScope(t *testing.T, kind string) task20HandleScope {
	t.Helper()
	baseline, err := task20CurrentProcessHandleIdentities()
	if err != nil {
		t.Fatalf("snapshot handles before %s: %v", kind, err)
	}
	return task20HandleScope{kind: kind, baseline: baseline}
}

func (scope *task20HandleScope) Capture(t *testing.T) {
	t.Helper()
	current, err := task20CurrentProcessHandleIdentities()
	if err != nil {
		t.Fatalf("snapshot handles during %s: %v", scope.kind, err)
	}
	for identity := range current {
		if _, existed := scope.baseline[identity]; existed {
			continue
		}
		handle := windows.Handle(identity.Value)
		if err := registerTask20OwnedHandle(handle, scope.kind); err != nil {
			t.Fatal(err)
		}
		scope.owned = append(scope.owned, identity)
	}
	if len(scope.owned) == 0 {
		t.Fatalf("%s opened no test-accounted Windows handles", scope.kind)
	}
}

func (scope *task20HandleScope) RequireClosed(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	remaining, err := scope.requireClosed(ctx, task20CurrentProcessHandleIdentities)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot handles after %s: %v", scope.kind, err)
	}
	for _, identity := range remaining {
		t.Errorf("%s handle %#x object %#x remains open", scope.kind, identity.Value, identity.Object)
	}
}

func (scope *task20HandleScope) requireClosed(ctx context.Context,
	query func() (map[task20HandleIdentity]struct{}, error)) ([]task20HandleIdentity, error) {
	remaining, err := task20WaitForHandleIdentities(ctx, scope.owned, query)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return remaining, err
	}
	remainingSet := make(map[task20HandleIdentity]struct{}, len(remaining))
	for _, identity := range remaining {
		remainingSet[identity] = struct{}{}
	}
	for _, identity := range scope.owned {
		if _, remains := remainingSet[identity]; !remains {
			deleteTask20OwnedHandle(identity.Value)
		}
	}
	return remaining, err
}

func task20WaitForHandleIdentities(ctx context.Context, owned []task20HandleIdentity,
	query func() (map[task20HandleIdentity]struct{}, error)) ([]task20HandleIdentity, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := query()
		if err != nil {
			return nil, err
		}
		remaining := make([]task20HandleIdentity, 0, len(owned))
		for _, identity := range owned {
			if _, remains := current[identity]; remains {
				remaining = append(remaining, identity)
			}
		}
		if len(remaining) == 0 {
			return nil, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return remaining, ctx.Err()
		}
	}
}

func deleteTask20OwnedHandle(value uintptr) {
	handle := windows.Handle(value)
	task20OwnedHandleRegistry.Lock()
	delete(task20OwnedHandleRegistry.handles, handle)
	task20OwnedHandleRegistry.Unlock()
}

func TestWindowsTask20HandleScopeWaitsForTransientClosure(t *testing.T) {
	owned := []task20HandleIdentity{{Value: 0x40, Object: 0x1000}}
	seedTask20OwnedHandle(t, owned[0])
	scope := task20HandleScope{owned: owned}
	calls := 0
	remaining, err := scope.requireClosed(context.Background(), func() (map[task20HandleIdentity]struct{}, error) {
		calls++
		if calls == 1 {
			return map[task20HandleIdentity]struct{}{owned[0]: {}}, nil
		}
		return map[task20HandleIdentity]struct{}{}, nil
	})
	if err != nil || len(remaining) != 0 || calls < 2 {
		t.Fatalf("remaining=%v err=%v calls=%d", remaining, err, calls)
	}
	if _, exists := snapshotTask20OwnedHandles()[windows.Handle(owned[0].Value)]; exists {
		t.Fatal("transient identity remained registered")
	}
}

func TestWindowsTask20HandleScopeReportsPersistentIdentity(t *testing.T) {
	owned := []task20HandleIdentity{{Value: 0x40, Object: 0x1000}}
	seedTask20OwnedHandle(t, owned[0])
	scope := task20HandleScope{owned: owned}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	remaining, err := scope.requireClosed(ctx, func() (map[task20HandleIdentity]struct{}, error) {
		return map[task20HandleIdentity]struct{}{owned[0]: {}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || len(remaining) != 1 || remaining[0] != owned[0] {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	if _, exists := snapshotTask20OwnedHandles()[windows.Handle(owned[0].Value)]; !exists {
		t.Fatal("persistent identity was unregistered")
	}
}

func TestWindowsTask20HandleScopeRetainsEvidenceOnQueryError(t *testing.T) {
	owned := []task20HandleIdentity{{Value: 0x40, Object: 0x1000}}
	seedTask20OwnedHandle(t, owned[0])
	scope := task20HandleScope{owned: owned}
	queryErr := errors.New("snapshot unavailable")
	remaining, err := scope.requireClosed(context.Background(), func() (map[task20HandleIdentity]struct{}, error) {
		return nil, queryErr
	})
	if !errors.Is(err, queryErr) || remaining != nil {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	if _, exists := snapshotTask20OwnedHandles()[windows.Handle(owned[0].Value)]; !exists {
		t.Fatal("query error unregistered indeterminate identity")
	}
}

func TestWindowsTask20HandleScopeTreatsReusedSlotAsClosed(t *testing.T) {
	owned := []task20HandleIdentity{{Value: 0x40, Object: 0x1000}}
	seedTask20OwnedHandle(t, owned[0])
	scope := task20HandleScope{owned: owned}
	remaining, err := scope.requireClosed(context.Background(), func() (map[task20HandleIdentity]struct{}, error) {
		return map[task20HandleIdentity]struct{}{{Value: owned[0].Value, Object: 0x2000}: {}}, nil
	})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	if _, exists := snapshotTask20OwnedHandles()[windows.Handle(owned[0].Value)]; exists {
		t.Fatal("reused numeric handle remained registered")
	}
}

func seedTask20OwnedHandle(t *testing.T, identity task20HandleIdentity) {
	t.Helper()
	handle := windows.Handle(identity.Value)
	if err := registerTask20OwnedHandle(handle, "scope-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteTask20OwnedHandle(identity.Value) })
}
