package session

import (
	"sync"
	"testing"
	"time"
)

type onceGate struct {
	channel chan struct{}
	once    sync.Once
}

func newOnceGate() *onceGate {
	return &onceGate{channel: make(chan struct{})}
}

func (gate *onceGate) Open() {
	gate.once.Do(func() { close(gate.channel) })
}

type replyCollector[T any] struct {
	input chan T
	stop  *onceGate
	done  chan struct{}

	mu      sync.Mutex
	replies []T
}

func newReplyCollector[T any]() *replyCollector[T] {
	collector := &replyCollector[T]{
		input: make(chan T),
		stop:  newOnceGate(),
		done:  make(chan struct{}),
	}
	go func() {
		defer close(collector.done)
		for {
			select {
			case reply := <-collector.input:
				collector.mu.Lock()
				collector.replies = append(collector.replies, reply)
				collector.mu.Unlock()
			case <-collector.stop.channel:
				return
			}
		}
	}()
	return collector
}

func (collector *replyCollector[T]) StopAndReplies(t *testing.T) []T {
	t.Helper()
	done := collector.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reply collector did not stop")
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]T(nil), collector.replies...)
}

func (collector *replyCollector[T]) Stop() <-chan struct{} {
	collector.stop.Open()
	return collector.done
}

func TestActorReplyCollectorDrainsUntilQuiescence(t *testing.T) {
	collector := newReplyCollector[int]()
	sent := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-sent:
		case <-time.After(2 * time.Second):
			t.Errorf("cleanup: collector sender did not exit")
		}
		collector.Stop()
		select {
		case <-collector.done:
		case <-time.After(2 * time.Second):
			t.Errorf("cleanup: collector did not join")
		}
	})
	go func() {
		defer close(sent)
		for value := range 1000 {
			collector.input <- value
		}
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("collector blocked a reply sender")
	}
	replies := collector.StopAndReplies(t)
	if len(replies) != 1000 {
		t.Fatalf("reply count = %d; want 1000", len(replies))
	}
}
