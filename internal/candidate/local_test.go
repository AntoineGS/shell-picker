package candidate

import "testing"

func TestLocalWorkerCountBounded(t *testing.T) {
	if got := localWorkerCount(0); got < 2 || got > 8 {
		t.Errorf("default localWorkerCount=%d outside 2..8", got)
	}
	for requested, want := range map[int]int{1: 2, 2: 2, 8: 8, 9: 8} {
		if got := localWorkerCount(requested); got != want {
			t.Errorf("localWorkerCount(%d)=%d want %d", requested, got, want)
		}
	}
}
