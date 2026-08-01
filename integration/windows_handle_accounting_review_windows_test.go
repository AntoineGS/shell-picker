//go:build windows

package integration

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTask20CapturedHandleClassificationRejectsReusedSlotObjectMismatch(t *testing.T) {
	captured := task20SystemHandleTableEntry{HandleValue: 0x40, Object: 0x1000}
	duplicate := windows.Handle(0x80)
	var closed []windows.Handle
	classified := false

	got, err := task20ClassifyCapturedHandleEntries([]task20SystemHandleTableEntry{captured}, task20HandlePinAPI{
		duplicate: func(handle windows.Handle) (windows.Handle, error) {
			if handle != windows.Handle(captured.HandleValue) {
				t.Fatalf("source handle=%#x want=%#x", uintptr(handle), captured.HandleValue)
			}
			return duplicate, nil
		},
		snapshot: func() ([]task20SystemHandleTableEntry, error) {
			return []task20SystemHandleTableEntry{{HandleValue: uintptr(duplicate), Object: 0x2000}}, nil
		},
		classify: func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error) {
			classified = true
			return task20ResourceIdentity{}, nil
		},
		close: func(handle windows.Handle) error {
			closed = append(closed, handle)
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "object mismatch") {
		t.Fatalf("result=%v err=%v; want an object-mismatch error", got, err)
	}
	if got != nil {
		t.Fatalf("classified=%v; want nil on mismatch", got)
	}
	if classified {
		t.Fatal("mismatched pinned object was classified")
	}
	if len(closed) != 1 || closed[0] != duplicate {
		t.Fatalf("closed=%v; want [%#x]", closed, uintptr(duplicate))
	}
}

func TestTask20CapturedHandleClassificationPreservesOriginalIdentity(t *testing.T) {
	captured := task20SystemHandleTableEntry{HandleValue: 0x40, Object: 0x1000}
	duplicate := windows.Handle(0x80)
	var classifiedHandle windows.Handle
	var classifiedIdentity task20HandleIdentity
	var closed []windows.Handle

	got, err := task20ClassifyCapturedHandleEntries([]task20SystemHandleTableEntry{captured}, task20HandlePinAPI{
		duplicate: func(windows.Handle) (windows.Handle, error) { return duplicate, nil },
		snapshot: func() ([]task20SystemHandleTableEntry, error) {
			return []task20SystemHandleTableEntry{{HandleValue: uintptr(duplicate), Object: captured.Object}}, nil
		},
		classify: func(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			classifiedHandle = handle
			classifiedIdentity = identity
			return task20TestResource(identity.Value, identity.Object, "File", task20HandleSocket), nil
		},
		close: func(handle windows.Handle) error {
			closed = append(closed, handle)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("classification failed: %v", err)
	}
	wantIdentity := task20HandleIdentity{Value: captured.HandleValue, Object: captured.Object}
	want := task20TestResource(wantIdentity.Value, wantIdentity.Object, "File", task20HandleSocket)
	if classifiedHandle != duplicate || classifiedIdentity != wantIdentity {
		t.Fatalf("classified handle=%#x identity=%+v; want handle=%#x identity=%+v", uintptr(classifiedHandle), classifiedIdentity, uintptr(duplicate), wantIdentity)
	}
	if len(got) != 1 || got[wantIdentity] != want || got[task20HandleIdentity{Value: uintptr(duplicate), Object: captured.Object}] != (task20ResourceIdentity{}) {
		t.Fatalf("classified=%v; want only original identity=%+v", got, want)
	}
	if len(closed) != 1 || closed[0] != duplicate {
		t.Fatalf("closed=%v; want [%#x]", closed, uintptr(duplicate))
	}
}

func TestTask20CapturedHandleClassificationFailsClosedOnDuplicateError(t *testing.T) {
	first := task20SystemHandleTableEntry{HandleValue: 0x40, Object: 0x1000}
	second := task20SystemHandleTableEntry{HandleValue: 0x44, Object: 0x1400}
	duplicate := windows.Handle(0x80)
	duplicateErr := errors.New("duplicate failed")
	var closed []windows.Handle

	got, err := task20ClassifyCapturedHandleEntries([]task20SystemHandleTableEntry{first, second}, task20HandlePinAPI{
		duplicate: func(handle windows.Handle) (windows.Handle, error) {
			if handle == windows.Handle(second.HandleValue) {
				return 0, duplicateErr
			}
			return duplicate, nil
		},
		snapshot: func() ([]task20SystemHandleTableEntry, error) {
			t.Fatal("object snapshot ran after duplicate failure")
			return nil, nil
		},
		classify: func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error) {
			t.Fatal("classification ran after duplicate failure")
			return task20ResourceIdentity{}, nil
		},
		close: func(handle windows.Handle) error {
			closed = append(closed, handle)
			return nil
		},
	})
	if !errors.Is(err, duplicateErr) || got != nil {
		t.Fatalf("result=%v err=%v; want duplicate failure and nil result", got, err)
	}
	if len(closed) != 1 || closed[0] != duplicate {
		t.Fatalf("closed=%v; want [%#x]", closed, uintptr(duplicate))
	}
}

func TestTask20CapturedHandleClassificationClosesPinsOnInvalidEntry(t *testing.T) {
	valid := task20SystemHandleTableEntry{HandleValue: 0x40, Object: 0x1000}
	invalid := task20SystemHandleTableEntry{HandleValue: 0x44, Object: 0}
	duplicate := windows.Handle(0x80)
	var closed []windows.Handle

	got, err := task20ClassifyCapturedHandleEntries([]task20SystemHandleTableEntry{valid, invalid}, task20HandlePinAPI{
		duplicate: func(windows.Handle) (windows.Handle, error) { return duplicate, nil },
		snapshot: func() ([]task20SystemHandleTableEntry, error) {
			t.Fatal("object snapshot ran after invalid captured entry")
			return nil, nil
		},
		classify: func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error) {
			t.Fatal("classification ran after invalid captured entry")
			return task20ResourceIdentity{}, nil
		},
		close: func(handle windows.Handle) error {
			closed = append(closed, handle)
			return nil
		},
	})
	if err == nil || got != nil {
		t.Fatalf("result=%v err=%v; want invalid identity error and nil result", got, err)
	}
	if len(closed) != 1 || closed[0] != duplicate {
		t.Fatalf("closed=%v; want [%#x]", closed, uintptr(duplicate))
	}
}

func TestTask20CapturedHandleClassificationFailsClosedOnObjectQueryError(t *testing.T) {
	captured := task20SystemHandleTableEntry{HandleValue: 0x40, Object: 0x1000}
	duplicate := windows.Handle(0x80)
	queryErr := errors.New("object snapshot failed")
	closed := false

	got, err := task20ClassifyCapturedHandleEntries([]task20SystemHandleTableEntry{captured}, task20HandlePinAPI{
		duplicate: func(windows.Handle) (windows.Handle, error) { return duplicate, nil },
		snapshot:  func() ([]task20SystemHandleTableEntry, error) { return nil, queryErr },
		classify: func(windows.Handle, task20HandleIdentity) (task20ResourceIdentity, error) {
			t.Fatal("classification ran after object query failure")
			return task20ResourceIdentity{}, nil
		},
		close: func(handle windows.Handle) error {
			closed = handle == duplicate
			return nil
		},
	})
	if !errors.Is(err, queryErr) || got != nil {
		t.Fatalf("result=%v err=%v; want object query failure and nil result", got, err)
	}
	if !closed {
		t.Fatal("pinned duplicate was not closed after object query failure")
	}
}

func TestTask20CapturedHandleClassificationFailsClosedOnCloseError(t *testing.T) {
	captured := task20SystemHandleTableEntry{HandleValue: 0x40, Object: 0x1000}
	duplicate := windows.Handle(0x80)
	closeErr := errors.New("close failed")

	got, err := task20ClassifyCapturedHandleEntries([]task20SystemHandleTableEntry{captured}, task20HandlePinAPI{
		duplicate: func(windows.Handle) (windows.Handle, error) { return duplicate, nil },
		snapshot: func() ([]task20SystemHandleTableEntry, error) {
			return []task20SystemHandleTableEntry{{HandleValue: uintptr(duplicate), Object: captured.Object}}, nil
		},
		classify: func(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
			return task20TestResource(identity.Value, identity.Object, "Process", task20HandleProcess), nil
		},
		close: func(windows.Handle) error { return closeErr },
	})
	if !errors.Is(err, closeErr) || got != nil {
		t.Fatalf("result=%v err=%v; want close failure and nil result", got, err)
	}
}
