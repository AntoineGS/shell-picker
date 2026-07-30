//go:build windows

package integration

import (
	"testing"

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
	current, err := task20CurrentProcessHandleIdentities()
	if err != nil {
		t.Fatalf("snapshot handles after %s: %v", scope.kind, err)
	}
	for _, identity := range scope.owned {
		handle := windows.Handle(identity.Value)
		if _, remains := current[identity]; remains {
			t.Errorf("%s handle %#x object %#x remains open", scope.kind, identity.Value, identity.Object)
			continue
		}
		task20OwnedHandleRegistry.Lock()
		delete(task20OwnedHandleRegistry.handles, handle)
		task20OwnedHandleRegistry.Unlock()
	}
}
