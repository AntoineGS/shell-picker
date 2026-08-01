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

	"golang.org/x/sys/windows"
)

func task20ApplicationHandles(classified map[task20HandleIdentity]task20ResourceIdentity) (map[task20HandleIdentity]task20ResourceIdentity, error) {
	application := make(map[task20HandleIdentity]task20ResourceIdentity, len(classified))
	for identity, resource := range classified {
		owned, err := resource.applicationOwned()
		if err != nil {
			resource.Identity = identity
			return nil, fmt.Errorf("determine ownership for %s: %w", task20ResourceDiagnostic(resource), err)
		}
		if !owned {
			continue
		}
		application[identity] = resource
	}
	return application, nil
}

func TestWindowsResourceGateReportsPersistentApplicationIdentityAfterFiltering(t *testing.T) {
	event := task20ResourceIdentity{Identity: task20HandleIdentity{Value: 0x41, Object: 0x3000}, Type: "Event", Kind: task20HandleEvent}
	socket := task20ResourceIdentity{Identity: task20HandleIdentity{Value: 0x40, Object: 0x2000}, Type: "File", Kind: task20HandleSocket}
	classified := map[task20HandleIdentity]task20ResourceIdentity{
		event.Identity:  event,
		socket.Identity: socket,
	}
	application, err := task20ApplicationHandles(classified)
	if err != nil {
		t.Fatal(err)
	}
	diff := resourceDifference(resourceSnapshot{}, resourceSnapshot{applicationHandles: application})
	want := "platform=handles baseline=0 current=0; added=[kind=Socket object_type=File value=0x40 object=0x2000] removed=[]"
	if diff != want {
		t.Fatalf("difference=%q want=%q", diff, want)
	}
}

func TestWindowsUnknownApplicationHandleKindFailsFiltering(t *testing.T) {
	unknown := task20ResourceIdentity{Identity: task20HandleIdentity{Value: 0x30, Object: 0x3000}, Type: "Future", Kind: task20HandleKind(255)}
	got, err := task20ApplicationHandles(map[task20HandleIdentity]task20ResourceIdentity{unknown.Identity: unknown})
	if err == nil || !strings.Contains(err.Error(), "unsupported Windows handle kind") {
		t.Fatalf("application handles=%v err=%v; want an ownership error", got, err)
	}
	if got != nil {
		t.Fatalf("application handles=%v; want nil on ownership error", got)
	}
}

func TestWindowsTask20ScopePolicyIsTypeExact(t *testing.T) {
	kinds := []task20HandleKind{
		task20HandleUnknown,
		task20HandleFile,
		task20HandlePipe,
		task20HandleSocket,
		task20HandleProcess,
		task20HandleJob,
		task20HandleThread,
		task20HandleEvent,
		task20HandleTimer,
		task20HandleIOCompletion,
		task20HandleWaitCompletion,
	}
	for _, phase := range []string{"server", "process/job", "unknown"} {
		for _, kind := range kinds {
			want := (phase == "server" && kind == task20HandleSocket) ||
				(phase == "process/job" && (kind == task20HandleProcess || kind == task20HandleJob))
			if got := task20ScopeTracks(phase, kind); got != want {
				t.Errorf("phase=%s kind=%v got=%v want=%v", phase, kind, got, want)
			}
		}
	}
}

func TestTask20HandleRegistryMetadataIncludesSemanticAndNativeType(t *testing.T) {
	resource := task20TestResource(0x40, 0x2000, "File", task20HandleSocket)
	want := "phase=server kind=Socket object_type=File value=0x40 object=0x2000"
	if got := task20HandleRegistryMetadata("server", resource); got != want {
		t.Fatalf("metadata=%q want=%q", got, want)
	}
}

func TestWindowsRawHandleCountDoesNotChangeApplicationDifference(t *testing.T) {
	resource := task20ResourceIdentity{Identity: task20HandleIdentity{Value: 0x40, Object: 0x4000}, Type: "Process", Kind: task20HandleProcess}
	application := map[task20HandleIdentity]task20ResourceIdentity{resource.Identity: resource}
	baseline := resourceSnapshot{handles: 1, applicationHandles: application}
	current := resourceSnapshot{handles: 2, applicationHandles: application}
	if diff := platformResourceDifference(baseline, current); diff != "" {
		t.Fatalf("platform difference=%q; want no difference", diff)
	}
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
			parts[index] = task20ResourceDiagnostic(resource)
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

func task20CurrentProcessApplicationHandles() (map[task20HandleIdentity]task20ResourceIdentity, error) {
	classified, err := task20CurrentProcessClassifiedHandles()
	if err != nil {
		return nil, err
	}
	return task20ApplicationHandles(classified)
}

type task20HandleScope struct {
	phase    string
	baseline map[task20HandleIdentity]task20ResourceIdentity
	owned    []task20ResourceIdentity
}

func task20ScopeTracks(phase string, kind task20HandleKind) bool {
	switch phase {
	case "server":
		return kind == task20HandleSocket
	case "process/job":
		return kind == task20HandleProcess || kind == task20HandleJob
	default:
		return false
	}
}

func task20HandleRegistryMetadata(phase string, resource task20ResourceIdentity) string {
	return fmt.Sprintf("phase=%s %s", phase, task20ResourceDiagnostic(resource))
}

func beginTask20HandleScope(t *testing.T, phase string) task20HandleScope {
	t.Helper()
	baseline, err := task20CurrentProcessClassifiedHandles()
	if err != nil {
		t.Fatalf("snapshot handles before %s: %v", phase, err)
	}
	return task20HandleScope{phase: phase, baseline: baseline}
}

func (scope *task20HandleScope) Capture(t *testing.T) {
	t.Helper()
	classified, err := task20CurrentProcessClassifiedHandles()
	if err != nil {
		t.Fatalf("snapshot handles during %s: %v", scope.phase, err)
	}
	current, err := task20ApplicationHandles(classified)
	if err != nil {
		t.Fatalf("filter handles during %s: %v", scope.phase, err)
	}
	for identity, resource := range current {
		resource.Identity = identity
		previous, existed := scope.baseline[identity]
		if existed && previous == resource {
			continue
		}
		if !task20ScopeTracks(scope.phase, resource.Kind) {
			continue
		}
		handle := windows.Handle(resource.Identity.Value)
		if err := registerTask20OwnedHandle(handle, task20HandleRegistryMetadata(scope.phase, resource)); err != nil {
			t.Fatal(err)
		}
		scope.owned = append(scope.owned, resource)
	}
	if len(scope.owned) == 0 {
		t.Fatalf("%s opened no test-accounted Windows handles", scope.phase)
	}
}

func (scope *task20HandleScope) RequireClosed(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	remaining, err := scope.requireClosed(ctx, task20CurrentProcessApplicationHandles)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot handles after %s: %v", scope.phase, err)
	}
	for _, resource := range remaining {
		t.Errorf("%s resource %s remains open", scope.phase, task20HandleRegistryMetadata(scope.phase, resource))
	}
}

func (scope *task20HandleScope) requireClosed(ctx context.Context,
	query func() (map[task20HandleIdentity]task20ResourceIdentity, error)) ([]task20ResourceIdentity, error) {
	remaining, err := task20WaitForClassifiedResources(ctx, scope.owned, query)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return remaining, err
	}
	remainingSet := make(map[task20ResourceIdentity]struct{}, len(remaining))
	for _, resource := range remaining {
		remainingSet[resource] = struct{}{}
	}
	for _, resource := range scope.owned {
		if _, remains := remainingSet[resource]; !remains {
			deleteTask20OwnedHandle(resource.Identity.Value)
		}
	}
	return remaining, err
}

func task20WaitForClassifiedResources(ctx context.Context, owned []task20ResourceIdentity,
	query func() (map[task20HandleIdentity]task20ResourceIdentity, error)) ([]task20ResourceIdentity, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := query()
		if err != nil {
			return nil, err
		}
		remaining := make([]task20ResourceIdentity, 0, len(owned))
		for _, resource := range owned {
			latest, remains := current[resource.Identity]
			if remains && latest == resource {
				remaining = append(remaining, resource)
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
	owned := []task20ResourceIdentity{task20TestResource(0x40, 0x1000, "Process", task20HandleProcess)}
	seedTask20OwnedHandle(t, "process/job", owned[0])
	scope := task20HandleScope{phase: "process/job", owned: owned}
	calls := 0
	remaining, err := scope.requireClosed(context.Background(), func() (map[task20HandleIdentity]task20ResourceIdentity, error) {
		calls++
		if calls == 1 {
			return map[task20HandleIdentity]task20ResourceIdentity{owned[0].Identity: owned[0]}, nil
		}
		return map[task20HandleIdentity]task20ResourceIdentity{}, nil
	})
	if err != nil || len(remaining) != 0 || calls < 2 {
		t.Fatalf("remaining=%v err=%v calls=%d", remaining, err, calls)
	}
	assertTask20OwnedHandleEvidence(t, "process/job", owned[0], false)
}

func TestWindowsTask20HandleScopeReportsPersistentIdentity(t *testing.T) {
	owned := []task20ResourceIdentity{task20TestResource(0x40, 0x1000, "Process", task20HandleProcess)}
	seedTask20OwnedHandle(t, "process/job", owned[0])
	scope := task20HandleScope{phase: "process/job", owned: owned}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	remaining, err := scope.requireClosed(ctx, func() (map[task20HandleIdentity]task20ResourceIdentity, error) {
		return map[task20HandleIdentity]task20ResourceIdentity{owned[0].Identity: owned[0]}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || len(remaining) != 1 || remaining[0] != owned[0] {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	assertTask20OwnedHandleEvidence(t, "process/job", owned[0], true)
}

func TestWindowsTask20HandleScopeRetainsEvidenceOnQueryError(t *testing.T) {
	owned := []task20ResourceIdentity{task20TestResource(0x40, 0x1000, "Process", task20HandleProcess)}
	seedTask20OwnedHandle(t, "process/job", owned[0])
	scope := task20HandleScope{phase: "process/job", owned: owned}
	queryErr := errors.New("snapshot unavailable")
	remaining, err := scope.requireClosed(context.Background(), func() (map[task20HandleIdentity]task20ResourceIdentity, error) {
		return nil, queryErr
	})
	if !errors.Is(err, queryErr) || remaining != nil {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	assertTask20OwnedHandleEvidence(t, "process/job", owned[0], true)
}

func TestWindowsTask20HandleScopeTreatsReusedSlotAsClosed(t *testing.T) {
	owned := []task20ResourceIdentity{task20TestResource(0x40, 0x1000, "Process", task20HandleProcess)}
	seedTask20OwnedHandle(t, "process/job", owned[0])
	scope := task20HandleScope{phase: "process/job", owned: owned}
	reused := task20TestResource(owned[0].Identity.Value, 0x2000, "Process", task20HandleProcess)
	remaining, err := scope.requireClosed(context.Background(), func() (map[task20HandleIdentity]task20ResourceIdentity, error) {
		return map[task20HandleIdentity]task20ResourceIdentity{reused.Identity: reused}, nil
	})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	assertTask20OwnedHandleEvidence(t, "process/job", owned[0], false)
}

func task20TestResource(value, object uintptr, objectType string, kind task20HandleKind) task20ResourceIdentity {
	identity := task20HandleIdentity{Value: value, Object: object}
	return task20ResourceIdentity{Identity: identity, Type: objectType, Kind: kind}
}

func seedTask20OwnedHandle(t *testing.T, phase string, resource task20ResourceIdentity) {
	t.Helper()
	handle := windows.Handle(resource.Identity.Value)
	metadata := task20HandleRegistryMetadata(phase, resource)
	if err := registerTask20OwnedHandle(handle, metadata); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteTask20OwnedHandle(resource.Identity.Value) })
}

func assertTask20OwnedHandleEvidence(t *testing.T, phase string, resource task20ResourceIdentity, wantRegistered bool) {
	t.Helper()
	metadata, registered := snapshotTask20OwnedHandles()[windows.Handle(resource.Identity.Value)]
	if registered != wantRegistered {
		t.Fatalf("handle %#x registered=%v want=%v", resource.Identity.Value, registered, wantRegistered)
	}
	if !wantRegistered {
		return
	}
	want := task20HandleRegistryMetadata(phase, resource)
	if metadata != want {
		t.Fatalf("handle %#x metadata=%q want=%q", resource.Identity.Value, metadata, want)
	}
}
