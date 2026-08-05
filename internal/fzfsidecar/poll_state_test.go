package fzfsidecar

import (
	"context"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSessionPostsInitialStateAndOnlyChangedLabels(t *testing.T) {
	manual := newManualTicker()
	server := newFakeFZFServer(t, func(getNumber int) string {
		if getNumber >= 3 {
			return `{"totalCount":42,"matchCount":8,"selected":[]}`
		}
		return `{"totalCount":42,"matchCount":7,"selected":[]}`
	})
	session := newServerSession(t, protocol.PickerCD, server, manual)

	session.Start(context.Background())
	if got := receiveTestValue(t, server.posted, "initial label POST"); got != "change-list-label:7/42" {
		t.Fatalf("initial action = %q, want %q", got, "change-list-label:7/42")
	}
	if got := receiveTestValue(t, server.gets, "initial label GET"); got != 1 {
		t.Fatalf("initial GET number = %d, want 1", got)
	}

	manual.Tick(time.Now())
	if got := receiveTestValue(t, server.gets, "equal-state GET"); got != 2 {
		t.Fatalf("equal-state GET number = %d, want 2", got)
	}

	manual.Tick(time.Now())
	if got := receiveTestValue(t, server.gets, "changed-state GET"); got != 3 {
		t.Fatalf("changed-state GET number = %d, want 3", got)
	}
	if got := receiveTestValue(t, server.posted, "changed label POST"); got != "change-list-label:8/42" {
		t.Fatalf("changed action = %q, want %q", got, "change-list-label:8/42")
	}
	if _, got := server.counts(); got != 2 {
		t.Fatalf("actions after equal state = %d, want 2 total actions", got)
	}

	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	getCount, postCount := server.counts()
	manual.Tick(time.Now())
	manual.Tick(time.Now())
	if got, _ := server.counts(); got != getCount {
		t.Fatalf("GETs after Wait = %d, want %d", got, getCount)
	}
	if _, got := server.counts(); got != postCount {
		t.Fatalf("POSTs after Wait = %d, want %d", got, postCount)
	}
}

func TestSessionDoesNotPostWhenRenderedLabelIsUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		picker protocol.Picker
		states []string
		label  string
	}{
		{name: "cd selected-only change", picker: protocol.PickerCD, states: []string{`{"totalCount":42,"matchCount":7,"selected":[]}`, `{"totalCount":42,"matchCount":7,"selected":[{}]}`}, label: "change-list-label:7/42"},
		{name: "cp selected-items change", picker: protocol.PickerCP, states: []string{`{"totalCount":42,"matchCount":7,"selected":[{"id":1},{"id":2}]}`, `{"totalCount":42,"matchCount":7,"selected":[{"id":3},{"id":4}]}`}, label: "change-list-label:7/42 (2)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manual := newManualTicker()
			server := newFakeFZFServer(t, func(getNumber int) string {
				if getNumber > len(test.states) {
					return test.states[len(test.states)-1]
				}
				return test.states[getNumber-1]
			})
			session := newServerSession(t, test.picker, server, manual)

			session.Start(context.Background())
			if got := receiveTestValue(t, server.posted, "initial unchanged-label POST"); got != test.label {
				t.Fatalf("initial action = %q, want %q", got, test.label)
			}
			if got := receiveTestValue(t, server.gets, "initial unchanged-label GET"); got != 1 {
				t.Fatalf("initial GET number = %d, want 1", got)
			}

			manual.Tick(time.Time{})
			if got := receiveTestValue(t, server.gets, "equal-label GET"); got != 2 {
				t.Fatalf("equal-label GET number = %d, want 2", got)
			}
			manual.Tick(time.Time{})
			if got := receiveTestValue(t, server.gets, "following unchanged-label GET"); got != 3 {
				t.Fatalf("following GET number = %d, want 3", got)
			}
			_, postCount := server.counts()
			if postCount != 1 {
				t.Fatalf("POST count after equal label = %d, want 1", postCount)
			}
		})
	}
}

func TestSessionWithPaginatedCPStatePostsOnlyChangedLabels(t *testing.T) {
	manual := newManualTicker()
	getEvents := make(chan dumpStatusCall, 16)
	postEvents := make(chan string, 8)
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, snapshots: []int{101, 101, 102}, getEvents: getEvents, postEvents: postEvents}
	session, err := New(protocol.PickerCP, WithTimer(func(time.Duration) timer { return manual }), WithTransport(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, postEvents, "initial paginated POST"); got != "change-list-label:7/200 (101)" {
		t.Fatalf("initial action = %q", got)
	}
	for range 2 {
		if got := receiveTestValue(t, getEvents, "initial paginated GET"); got.offset != 0 && got.offset != 100 {
			t.Fatalf("initial GET offset = %d", got.offset)
		}
	}

	manual.Tick(time.Time{})
	for range 2 {
		receiveTestValue(t, getEvents, "equal-label paginated GET")
	}
	manual.Tick(time.Time{})
	select {
	case got := <-postEvents:
		t.Fatalf("equal-label action = %q", got)
	case got := <-getEvents:
		if got.offset != 0 {
			t.Fatalf("next-cycle GET offset = %d, want 0", got.offset)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for next-cycle GET")
	}
	if got := receiveTestValue(t, getEvents, "changed-label paginated second GET"); got.offset != 100 {
		t.Fatalf("next-cycle GET offset = %d, want 100", got.offset)
	}
	if got := receiveTestValue(t, postEvents, "changed-label paginated POST"); got != "change-list-label:7/200 (102)" {
		t.Fatalf("changed action = %q", got)
	}

	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionRetriesInconsistentPaginatedSnapshotOnTheNextTick(t *testing.T) {
	manual := newManualTicker()
	getEvents := make(chan dumpStatusCall, 8)
	postEvents := make(chan string, 4)
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, snapshots: []int{101, 102}, mutationOffset: 100, mutationTotal: 201, mutationMatch: 7, mutateFirstCycle: true, getEvents: getEvents, postEvents: postEvents}
	session, err := New(protocol.PickerCP, WithTimer(func(time.Duration) timer { return manual }), WithTransport(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		session.Stop()
		_ = session.Wait()
	})
	session.Start(context.Background())
	for range 2 {
		receiveTestValue(t, getEvents, "inconsistent snapshot GET")
	}
	select {
	case got := <-postEvents:
		t.Fatalf("mutation-cycle action = %q, want no POST", got)
	default:
	}

	manual.Tick(time.Time{})
	for range 2 {
		receiveTestValue(t, getEvents, "replayed snapshot GET")
	}
	if got := receiveTestValue(t, postEvents, "replayed snapshot POST"); got != "change-list-label:7/200 (102)" {
		t.Fatalf("stable-cycle action = %q, want selected count 102", got)
	}
}

func TestSessionDoesNotPostWhenSelectedPaginationReachesThePageCap(t *testing.T) {
	getEvents := make(chan dumpStatusCall, maxSelectedPages)
	postEvents := make(chan string, 1)
	fake := &dumpStatusFake{totalCount: selectedPageSize * maxSelectedPages, matchCount: 7, selectedCount: selectedPageSize * maxSelectedPages, getEvents: getEvents, postEvents: postEvents}
	session, err := New(protocol.PickerCP, WithTransport(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	select {
	case got := <-postEvents:
		t.Fatalf("page-cap action = %q, want no POST", got)
	default:
	}
	if calls := fake.callsSnapshot(); len(calls) != maxSelectedPages {
		t.Fatalf("GET calls = %d, want %d", len(calls), maxSelectedPages)
	}
}
