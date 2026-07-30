//go:build windows

package integration

import (
	"testing"

	"golang.org/x/sys/windows"
)

type task20HandleScope struct {
	kind     string
	baseline map[uintptr]struct{}
	owned    []windows.Handle
}

func beginTask20HandleScope(t *testing.T, kind string) task20HandleScope {
	t.Helper()
	baseline, err := task20CurrentProcessHandleValues()
	if err != nil {
		t.Fatalf("snapshot handles before %s: %v", kind, err)
	}
	return task20HandleScope{kind: kind, baseline: baseline}
}

func (scope *task20HandleScope) Capture(t *testing.T) {
	t.Helper()
	current, err := task20CurrentProcessHandleValues()
	if err != nil {
		t.Fatalf("snapshot handles during %s: %v", scope.kind, err)
	}
	for value := range current {
		if _, existed := scope.baseline[value]; existed {
			continue
		}
		handle := windows.Handle(value)
		if err := registerTask20OwnedHandle(handle, scope.kind); err != nil {
			t.Fatal(err)
		}
		scope.owned = append(scope.owned, handle)
	}
	if len(scope.owned) == 0 {
		t.Fatalf("%s opened no test-accounted Windows handles", scope.kind)
	}
}

func (scope *task20HandleScope) RequireClosed(t *testing.T) {
	t.Helper()
	current, err := task20CurrentProcessHandleValues()
	if err != nil {
		t.Fatalf("snapshot handles after %s: %v", scope.kind, err)
	}
	for _, handle := range scope.owned {
		if _, remains := current[uintptr(handle)]; remains {
			t.Errorf("%s handle %#x remains open", scope.kind, uintptr(handle))
			continue
		}
		task20OwnedHandleRegistry.Lock()
		delete(task20OwnedHandleRegistry.handles, handle)
		task20OwnedHandleRegistry.Unlock()
	}
}
