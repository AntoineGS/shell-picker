package integration

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const descendantRecorderInterval = 2 * time.Millisecond

type descendantProcessRecord struct {
	PID             int
	Identity        string
	CommandLine     string
	FirstObservedAt time.Time
}

type descendantSnapshot func(int) ([]descendantProcessRecord, error)

type descendantRecorder struct {
	snapshot  descendantSnapshot
	rootReady chan struct{}
	capture   chan struct{}
	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once

	mu      sync.Mutex
	started bool
	stopped bool
	root    int
	rootSet bool
	records map[string]descendantProcessRecord
}

func newDescendantRecorder(snapshot descendantSnapshot) *descendantRecorder {
	return &descendantRecorder{snapshot: snapshot, rootReady: make(chan struct{}), capture: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), records: make(map[string]descendantProcessRecord)}
}

func (recorder *descendantRecorder) Start() {
	recorder.startOnce.Do(func() {
		recorder.mu.Lock()
		recorder.started = true
		recorder.mu.Unlock()
		go recorder.run()
	})
}

func (recorder *descendantRecorder) SetRoot(pid int) {
	if pid <= 0 {
		return
	}
	recorder.mu.Lock()
	if !recorder.rootSet {
		recorder.root, recorder.rootSet = pid, true
		close(recorder.rootReady)
	}
	recorder.mu.Unlock()
}

func (recorder *descendantRecorder) Capture() {
	recorder.mu.Lock()
	started, stopped := recorder.started, recorder.stopped
	recorder.mu.Unlock()
	if !started || stopped {
		return
	}
	select {
	case recorder.capture <- struct{}{}:
	default:
	}
}

func (recorder *descendantRecorder) CaptureAndWait() {
	recorder.captureNow()
}

func (recorder *descendantRecorder) StopAndJoin() {
	recorder.mu.Lock()
	started := recorder.started
	recorder.stopped = true
	recorder.mu.Unlock()
	if !started {
		return
	}
	recorder.stopOnce.Do(func() { close(recorder.stop) })
	<-recorder.done
	recorder.captureNow()
}

func (recorder *descendantRecorder) Records() []descendantProcessRecord {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	result := make([]descendantProcessRecord, 0, len(recorder.records))
	for _, record := range recorder.records {
		result = append(result, record)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].PID != result[right].PID {
			return result[left].PID < result[right].PID
		}
		return result[left].Identity < result[right].Identity
	})
	return result
}

func (recorder *descendantRecorder) run() {
	defer close(recorder.done)
	select {
	case <-recorder.rootReady:
	case <-recorder.stop:
		return
	}
	recorder.captureNow()
	ticker := time.NewTicker(descendantRecorderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-recorder.capture:
			recorder.captureNow()
		case <-ticker.C:
			recorder.captureNow()
		case <-recorder.stop:
			return
		}
	}
}

func (recorder *descendantRecorder) captureNow() {
	recorder.mu.Lock()
	root, rootSet := recorder.root, recorder.rootSet
	recorder.mu.Unlock()
	if !rootSet || recorder.snapshot == nil {
		return
	}
	records, err := recorder.snapshot(root)
	if err != nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, record := range records {
		key := fmt.Sprintf("%d\x00%s", record.PID, record.Identity)
		if previous, ok := recorder.records[key]; ok {
			if record.CommandLine == "" {
				record.CommandLine = previous.CommandLine
			}
			if !previous.FirstObservedAt.IsZero() {
				record.FirstObservedAt = previous.FirstObservedAt
			}
		}
		if record.FirstObservedAt.IsZero() {
			record.FirstObservedAt = time.Now()
		}
		recorder.records[key] = record
	}
}

func TestDescendantRecorderRetainsIdentityAndCommandUnionUntilJoined(t *testing.T) {
	var calls atomic.Int32
	recorder := newDescendantRecorder(func(root int) ([]descendantProcessRecord, error) {
		if root != 42 {
			t.Fatalf("snapshot root=%d, want 42", root)
		}
		switch calls.Add(1) {
		case 1:
			return []descendantProcessRecord{{PID: 43, Identity: "43:first", CommandLine: "fzf --listen=127.0.0.1:1"}}, nil
		case 2:
			return []descendantProcessRecord{{PID: 44, Identity: "44:callback", CommandLine: "shell-picker preview"}}, nil
		default:
			return []descendantProcessRecord{{PID: 45, Identity: "45:renderer", CommandLine: "eza --long"}}, nil
		}
	})
	recorder.Start()
	recorder.SetRoot(42)
	for range 3 {
		recorder.CaptureAndWait()
	}
	recorder.StopAndJoin()

	want := []descendantProcessRecord{
		{PID: 43, Identity: "43:first", CommandLine: "fzf --listen=127.0.0.1:1"},
		{PID: 44, Identity: "44:callback", CommandLine: "shell-picker preview"},
		{PID: 45, Identity: "45:renderer", CommandLine: "eza --long"},
	}
	got := recorder.Records()
	if len(got) != len(want) {
		t.Fatalf("records=%+v, want %+v", got, want)
	}
	for index := range got {
		if got[index].FirstObservedAt.IsZero() {
			t.Fatalf("record %d has no first observation timestamp: %+v", index, got[index])
		}
		want[index].FirstObservedAt = got[index].FirstObservedAt
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records=%+v, want %+v", got, want)
	}
}

func TestDescendantRecorderPreservesFirstObservedAtForStableIdentity(t *testing.T) {
	var calls atomic.Int32
	recorder := newDescendantRecorder(func(root int) ([]descendantProcessRecord, error) {
		if root != 42 {
			t.Fatalf("snapshot root=%d, want 42", root)
		}
		if calls.Add(1) == 1 {
			return []descendantProcessRecord{{PID: 43, Identity: "43:stable", CommandLine: "initial"}}, nil
		}
		return []descendantProcessRecord{{PID: 43, Identity: "43:stable", CommandLine: "updated"}}, nil
	})
	recorder.SetRoot(42)
	recorder.captureNow()
	first := recorder.Records()
	if len(first) != 1 || first[0].CommandLine != "initial" {
		t.Fatalf("first records=%+v, want initial stable record", first)
	}
	if first[0].FirstObservedAt.IsZero() {
		t.Fatal("first observation timestamp is zero")
	}
	recorder.captureNow()
	second := recorder.Records()
	if len(second) != 1 || second[0].CommandLine != "updated" {
		t.Fatalf("second records=%+v, want updated stable record", second)
	}
	if !second[0].FirstObservedAt.Equal(first[0].FirstObservedAt) {
		t.Fatalf("first observation changed from %s to %s", first[0].FirstObservedAt, second[0].FirstObservedAt)
	}
}
