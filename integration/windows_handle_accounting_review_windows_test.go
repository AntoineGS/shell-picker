//go:build windows

package integration

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTask20ClassifyHandleRecognizesIRTimer(t *testing.T) {
	identity := task20HandleIdentity{Value: 0x1234, Object: 0x5678}
	got, err := task20ClassifyHandleWith(windows.Handle(identity.Value), identity, task20HandleClassificationAPI{
		queryObjectType: func(windows.Handle) (string, error) { return "IRTimer", nil },
		getsockname: func(windows.Handle) (windows.Sockaddr, error) {
			t.Fatal("socket probe ran for IRTimer")
			return nil, nil
		},
		getFileType: func(windows.Handle) (uint32, error) {
			t.Fatal("file type probe ran for IRTimer")
			return 0, nil
		},
	})
	if err != nil {
		t.Fatalf("classify IRTimer: %v", err)
	}
	want := task20ResourceIdentity{Identity: identity, Type: "IRTimer", Kind: task20HandleTimer}
	if got != want {
		t.Fatalf("classification=%+v; want=%+v", got, want)
	}
	owned, err := got.applicationOwned()
	if err != nil || owned {
		t.Fatalf("IRTimer ownership=%v err=%v; want infrastructure resource", owned, err)
	}
}

func TestTask20CoherentHandleSnapshotStableSuccess(t *testing.T) {
	process := task20HandleIdentity{Value: 0x40, Object: 0x1000}
	socket := task20HandleIdentity{Value: 0x44, Object: 0x1400}
	set := task20TestHandleIdentitySet(process, socket)
	snapshotCalls := 0
	classifyCalls := 0

	got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
		snapshot: func() (map[task20HandleIdentity]struct{}, error) {
			snapshotCalls++
			return task20CloneHandleIdentitySet(set), nil
		},
		classify: func(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			classifyCalls++
			if uintptr(handle) != identity.Value {
				t.Fatalf("classifier handle=%#x identity=%+v", uintptr(handle), identity)
			}
			if identity == socket {
				return task20TestResource(identity.Value, identity.Object, "File", task20HandleSocket), nil
			}
			return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
		},
	})
	if err != nil {
		t.Fatalf("classification failed: %v", err)
	}
	want := map[task20HandleIdentity]task20ResourceIdentity{
		process: task20TestResource(process.Value, process.Object, "Process", task20HandleProcess),
		socket:  task20TestResource(socket.Value, socket.Object, "File", task20HandleSocket),
	}
	if !reflect.DeepEqual(got, want) || snapshotCalls != 2 || classifyCalls != 2 {
		t.Fatalf("classified=%v snapshots=%d classifications=%d; want=%v snapshots=2 classifications=2", got, snapshotCalls, classifyCalls, want)
	}
}

func TestTask20CoherentHandleSnapshotRetriesSameSlotObjectReuse(t *testing.T) {
	first := task20HandleIdentity{Value: 0x40, Object: 0x1000}
	second := task20HandleIdentity{Value: first.Value, Object: 0x2000}
	classifyCalls := []task20HandleIdentity{}
	snapshot := task20SnapshotSequence(t,
		task20TestHandleIdentitySet(first),
		task20TestHandleIdentitySet(second),
		task20TestHandleIdentitySet(second),
		task20TestHandleIdentitySet(second),
	)

	got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
		snapshot: snapshot,
		classify: func(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			if uintptr(handle) != identity.Value {
				t.Fatalf("classifier handle=%#x identity=%+v", uintptr(handle), identity)
			}
			classifyCalls = append(classifyCalls, identity)
			return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
		},
	})
	if err != nil {
		t.Fatalf("classification failed: %v", err)
	}
	want := map[task20HandleIdentity]task20ResourceIdentity{second: task20TestResource(second.Value, second.Object, "Process", task20HandleProcess)}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(classifyCalls, []task20HandleIdentity{first, second}) {
		t.Fatalf("classified=%v calls=%v; want=%v calls=[%+v %+v]", got, classifyCalls, want, first, second)
	}
}

func TestTask20CoherentHandleSnapshotRetriesAddedAndRemovedEntries(t *testing.T) {
	first := task20HandleIdentity{Value: 0x40, Object: 0x1000}
	added := task20HandleIdentity{Value: 0x44, Object: 0x1400}
	removed := task20HandleIdentity{Value: 0x48, Object: 0x1800}
	for _, test := range []struct {
		name      string
		snapshots []map[task20HandleIdentity]struct{}
		want      map[task20HandleIdentity]task20ResourceIdentity
	}{
		{
			name: "added entry",
			snapshots: []map[task20HandleIdentity]struct{}{
				task20TestHandleIdentitySet(first),
				task20TestHandleIdentitySet(first, added),
				task20TestHandleIdentitySet(first, added),
				task20TestHandleIdentitySet(first, added),
			},
			want: map[task20HandleIdentity]task20ResourceIdentity{
				first: task20TestResource(first.Value, first.Object, "Process", task20HandleProcess),
				added: task20TestResource(added.Value, added.Object, "Process", task20HandleProcess),
			},
		},
		{
			name: "removed entry",
			snapshots: []map[task20HandleIdentity]struct{}{
				task20TestHandleIdentitySet(first, removed),
				task20TestHandleIdentitySet(first),
				task20TestHandleIdentitySet(first),
				task20TestHandleIdentitySet(first),
			},
			want: map[task20HandleIdentity]task20ResourceIdentity{
				first: task20TestResource(first.Value, first.Object, "Process", task20HandleProcess),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
				snapshot: task20SnapshotSequence(t, test.snapshots...),
				classify: func(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
					if uintptr(handle) != identity.Value {
						t.Fatalf("classifier handle=%#x identity=%+v", uintptr(handle), identity)
					}
					return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
				},
			})
			if err != nil {
				t.Fatalf("classification failed: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("classified=%v; want=%v", got, test.want)
			}
		})
	}
}

func TestTask20CoherentHandleSnapshotRetryExhaustionReportsExactSets(t *testing.T) {
	first := task20HandleIdentity{Value: 0x40, Object: 0x1000}
	second := task20HandleIdentity{Value: first.Value, Object: 0x2000}
	third := task20HandleIdentity{Value: 0x44, Object: 0x3000}
	got, err := task20ClassifyCoherentHandleSnapshotWith(2, task20HandleSnapshotAPI{
		snapshot: task20SnapshotSequence(t,
			task20TestHandleIdentitySet(first),
			task20TestHandleIdentitySet(second),
			task20TestHandleIdentitySet(second),
			task20TestHandleIdentitySet(third),
		),
		classify: func(_ windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
		},
	})
	if got != nil {
		t.Fatalf("classified=%v; want nil after retry exhaustion", got)
	}
	want := "coherent current-process handle snapshot exhausted after 2 attempts: before=[value=0x40 object=0x2000] after=[value=0x44 object=0x3000]; difference=added=[value=0x44 object=0x3000] removed=[value=0x40 object=0x2000]"
	if err == nil || err.Error() != want {
		t.Fatalf("err=%q want=%q", err, want)
	}
}

func TestTask20CoherentHandleSnapshotClassifierErrorFailsClosed(t *testing.T) {
	wantErr := errors.New("classifier failed")
	identities := task20TestHandleIdentitySet(
		task20HandleIdentity{Value: 0x40, Object: 0x1000},
		task20HandleIdentity{Value: 0x44, Object: 0x1400},
	)
	snapshotCalls := 0
	classifyCalls := 0
	got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
		snapshot: func() (map[task20HandleIdentity]struct{}, error) {
			snapshotCalls++
			return task20CloneHandleIdentitySet(identities), nil
		},
		classify: func(_ windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			classifyCalls++
			if classifyCalls == 1 {
				return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
			}
			return task20ResourceIdentity{}, wantErr
		},
	})
	if got != nil || !errors.Is(err, wantErr) || snapshotCalls != 1 || classifyCalls != 2 {
		t.Fatalf("classified=%v err=%v snapshots=%d classifications=%d; want nil, classifier error, one snapshot, two classifications", got, err, snapshotCalls, classifyCalls)
	}
}

func TestTask20CoherentHandleSnapshotClassifierIdentityMutationFailsClosed(t *testing.T) {
	identity := task20HandleIdentity{Value: 0x40, Object: 0x1000}
	afterCalls := 0
	got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
		snapshot: func() (map[task20HandleIdentity]struct{}, error) {
			afterCalls++
			return task20TestHandleIdentitySet(identity), nil
		},
		classify: func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error) {
			return task20TestResource(identity.Value, 0x2000, "Process", task20HandleProcess), nil
		},
	})
	if got != nil {
		t.Fatalf("classified=%v; want nil on classifier identity mutation", got)
	}
	if err == nil || !strings.Contains(err.Error(), "changed original identity") || afterCalls != 1 {
		t.Fatalf("err=%v afterSnapshots=%d; want identity-mutation error and no post snapshot", err, afterCalls)
	}
}

func TestTask20CoherentHandleSnapshotQueryErrorFailsClosed(t *testing.T) {
	wantErr := errors.New("snapshot failed")
	snapshotCalls := 0
	got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
		snapshot: func() (map[task20HandleIdentity]struct{}, error) {
			snapshotCalls++
			if snapshotCalls == 2 {
				return nil, wantErr
			}
			return task20TestHandleIdentitySet(task20HandleIdentity{Value: 0x40, Object: 0x1000}), nil
		},
		classify: func(_ windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
		},
	})
	if got != nil || !errors.Is(err, wantErr) || snapshotCalls != 2 {
		t.Fatalf("classified=%v err=%v snapshots=%d; want nil, snapshot error, and two snapshots", got, err, snapshotCalls)
	}
}

func TestTask20CoherentHandleSnapshotRejectsInvalidCapturedIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		identity task20HandleIdentity
	}{
		{name: "zero value", identity: task20HandleIdentity{}},
		{name: "zero handle", identity: task20HandleIdentity{Object: 0x1000}},
		{name: "zero object", identity: task20HandleIdentity{Value: 0x40}},
	} {
		t.Run(test.name, func(t *testing.T) {
			classified := false
			got, err := task20ClassifyCoherentHandleSnapshotWith(3, task20HandleSnapshotAPI{
				snapshot: func() (map[task20HandleIdentity]struct{}, error) {
					return task20TestHandleIdentitySet(test.identity), nil
				},
				classify: func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error) {
					classified = true
					return task20ResourceIdentity{}, nil
				},
			})
			if got != nil || err == nil || classified {
				t.Fatalf("result=%v err=%v classified=%v; want nil, invalid-identity error, and no classification", got, err, classified)
			}
		})
	}
}

func task20TestHandleIdentitySet(identities ...task20HandleIdentity) map[task20HandleIdentity]struct{} {
	set := make(map[task20HandleIdentity]struct{}, len(identities))
	for _, identity := range identities {
		set[identity] = struct{}{}
	}
	return set
}

func task20CloneHandleIdentitySet(input map[task20HandleIdentity]struct{}) map[task20HandleIdentity]struct{} {
	clone := make(map[task20HandleIdentity]struct{}, len(input))
	for identity := range input {
		clone[identity] = struct{}{}
	}
	return clone
}

func task20SnapshotSequence(t *testing.T, snapshots ...map[task20HandleIdentity]struct{}) func() (map[task20HandleIdentity]struct{}, error) {
	t.Helper()
	index := 0
	return func() (map[task20HandleIdentity]struct{}, error) {
		if index == len(snapshots) {
			t.Fatalf("snapshot sequence exhausted at call %d", index+1)
		}
		current := task20CloneHandleIdentitySet(snapshots[index])
		index++
		return current, nil
	}
}
