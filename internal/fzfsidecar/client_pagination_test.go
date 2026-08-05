package fzfsidecar

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/finderinfo"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestGetStatePaginatesFZFSelectedDumpInFixedPages(t *testing.T) {
	tests := []struct {
		selected int
		offsets  []int
		label    string
	}{
		{selected: 0, offsets: []int{0}, label: "7/200"},
		{selected: 1, offsets: []int{0}, label: "7/200 (1)"},
		{selected: 100, offsets: []int{0, 100}, label: "7/200 (100)"},
		{selected: 101, offsets: []int{0, 100}, label: "7/200 (101)"},
		{selected: 200, offsets: []int{0, 100, 200}, label: "7/200 (200)"},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.selected), func(t *testing.T) {
			fake := &dumpStatusFake{totalCount: 200, matchCount: 7, selectedCount: test.selected}
			client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)

			state, err := client.getState(context.Background())
			if err != nil {
				t.Fatalf("getState: %v", err)
			}
			if state.formatted != test.label {
				t.Fatalf("formatted label = %q, want %q", state.formatted, test.label)
			}
			calls := fake.callsSnapshot()
			if len(calls) != len(test.offsets) {
				t.Fatalf("GET calls = %d, want %d (%v)", len(calls), len(test.offsets), calls)
			}
			for index, call := range calls {
				if call.limit != selectedPageSize || call.offset != test.offsets[index] {
					t.Errorf("GET[%d] = limit=%d offset=%d, want limit=%d offset=%d", index, call.limit, call.offset, selectedPageSize, test.offsets[index])
				}
			}
			if strings.Contains(state.formatted, "dump-item") {
				t.Fatal("selected item text escaped the decoded state")
			}
		})
	}
}

func TestGetStateCDUsesOnlyTheFirstSelectedPage(t *testing.T) {
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, selectedCount: 200}
	client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCD)

	state, err := client.getState(context.Background())
	if err != nil {
		t.Fatalf("getState: %v", err)
	}
	if state.formatted != "7/200" || state.selected != 0 {
		t.Fatalf("CD state = %+v, want formatted 7/200 and selected 0", state)
	}
	if calls := fake.callsSnapshot(); len(calls) != 1 || calls[0].offset != 0 {
		t.Fatalf("CD GET calls = %v, want one first page", calls)
	}
}

func TestGetStateRejectsCountMutationBetweenSelectedPages(t *testing.T) {
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, selectedCount: 101, mutationOffset: 100, mutationTotal: 201, mutationMatch: 7}
	client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)

	if _, err := client.getState(context.Background()); err == nil {
		t.Fatal("getState accepted count mutation between selected pages")
	}
	calls := fake.callsSnapshot()
	if len(calls) != 2 || calls[0].offset != 0 || calls[1].offset != 100 {
		t.Fatalf("GET calls = %v, want offsets [0 100]", calls)
	}
}

func TestGetStateCancellationStopsSelectedPagination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, selectedCount: 101, cancelAfterFirst: true, cancel: cancel}
	client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)

	if _, err := client.getState(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("getState error = %v, want context.Canceled", err)
	}
	if calls := fake.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("GET calls after cancellation = %d, want 1", len(calls))
	}
}

func TestGetStateAppliesBodyLimitToEverySelectedPage(t *testing.T) {
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, selectedCount: 101, oversizedOffset: 100}
	client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)

	if _, err := client.getState(context.Background()); !errors.Is(err, errResponseTooBig) {
		t.Fatalf("getState error = %v, want response-too-big", err)
	}
	calls := fake.callsSnapshot()
	if len(calls) != 2 || calls[0].offset != 0 || calls[1].offset != 100 {
		t.Fatalf("GET calls = %v, want first and second bounded pages", calls)
	}
}

func TestGetStateRejectsMaxCountAndPageBoundViolations(t *testing.T) {
	tests := []struct {
		name          string
		totalCount    int
		selectedCount int
		overPageCount int
	}{
		{name: "total max count", totalCount: finderinfo.MaxCount + 1},
		{name: "page max size", totalCount: 200, selectedCount: 100, overPageCount: selectedPageSize + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &dumpStatusFake{totalCount: test.totalCount, matchCount: 7, selectedCount: test.selectedCount, overPageCount: test.overPageCount}
			client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)
			if _, err := client.getState(context.Background()); err == nil {
				t.Fatal("getState accepted an out-of-bounds dumpStatus page")
			}
			calls := fake.callsSnapshot()
			if len(calls) != 1 || calls[0].limit != selectedPageSize || calls[0].offset != 0 {
				t.Fatalf("GET calls = %v, want one bounded page at offset 0", calls)
			}
		})
	}
}

func TestGetStateBoundsSelectedPaginationAtTheCyclePageCap(t *testing.T) {
	const expectedMaxPages = 100
	for _, extra := range []int{0, 1} {
		t.Run(strconv.Itoa(extra), func(t *testing.T) {
			fake := &dumpStatusFake{totalCount: selectedPageSize*expectedMaxPages + extra, matchCount: 7, selectedCount: selectedPageSize*expectedMaxPages + extra}
			client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)

			if _, err := client.getState(context.Background()); !errors.Is(err, errStateTooLarge) {
				t.Fatalf("getState error = %v, want selected-state-too-large", err)
			}
			calls := fake.callsSnapshot()
			if len(calls) != expectedMaxPages {
				t.Fatalf("GET calls = %d, want exact cap %d", len(calls), expectedMaxPages)
			}
		})
	}
}

func TestGetStateStopsWhenTheAggregatePaginationContextExpires(t *testing.T) {
	var aggregateCancel context.CancelFunc
	var observedTimeout time.Duration
	fake := &dumpStatusFake{totalCount: 200, matchCount: 7, selectedCount: 101, cancelAfterFirst: true, cancel: func() {
		if aggregateCancel != nil {
			aggregateCancel()
		}
	}}
	client := newClient("127.0.0.1:12345", "secret", newHTTPClientForTest(fake), nil, protocol.PickerCP)
	client.withTimeout = func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
		observedTimeout = timeout
		cycleContext, cancel := context.WithCancel(parent)
		aggregateCancel = cancel
		return cycleContext, cancel
	}

	if _, err := client.getState(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("getState error = %v, want aggregate context cancellation", err)
	}
	if observedTimeout != selectedPaginationTimeout {
		t.Fatalf("aggregate timeout = %v, want %v", observedTimeout, selectedPaginationTimeout)
	}
	if observedTimeout <= requestTimeout {
		t.Fatalf("aggregate timeout = %v, want longer than per-page timeout %v", observedTimeout, requestTimeout)
	}
	if calls := fake.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("GET calls after aggregate deadline = %d, want 1", calls)
	}
}
