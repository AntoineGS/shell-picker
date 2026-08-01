//go:build windows

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

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
	remaining, err := task20WaitForHandleIdentities(ctx, scope.owned, task20CurrentProcessHandleIdentities)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot handles after %s: %v", scope.kind, err)
	}
	remainingSet := make(map[task20HandleIdentity]struct{}, len(remaining))
	for _, identity := range remaining {
		remainingSet[identity] = struct{}{}
	}
	for _, identity := range scope.owned {
		if _, remains := remainingSet[identity]; remains {
			t.Errorf("%s handle %#x object %#x remains open", scope.kind, identity.Value, identity.Object)
			continue
		}
		deleteTask20OwnedHandle(identity.Value)
	}
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
	calls := 0
	remaining, err := task20WaitForHandleIdentities(context.Background(), owned, func() (map[task20HandleIdentity]struct{}, error) {
		calls++
		if calls == 1 {
			return map[task20HandleIdentity]struct{}{owned[0]: {}}, nil
		}
		return map[task20HandleIdentity]struct{}{}, nil
	})
	if err != nil || len(remaining) != 0 || calls < 2 {
		t.Fatalf("remaining=%v err=%v calls=%d", remaining, err, calls)
	}
}

func TestWindowsTask20HandleScopeReportsPersistentIdentity(t *testing.T) {
	owned := []task20HandleIdentity{{Value: 0x40, Object: 0x1000}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	remaining, err := task20WaitForHandleIdentities(ctx, owned, func() (map[task20HandleIdentity]struct{}, error) {
		return map[task20HandleIdentity]struct{}{owned[0]: {}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || len(remaining) != 1 || remaining[0] != owned[0] {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
}
