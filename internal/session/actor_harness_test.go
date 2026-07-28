package session

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
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

type cleanupJoin struct {
	name   string
	done   <-chan struct{}
	before func()
}

func runActorCleanup(actions []func(), joins []cleanupJoin, timeout time.Duration) []string {
	for _, action := range actions {
		action()
	}
	diagnostics := make([]string, 0)
	for _, join := range joins {
		if join.before != nil {
			join.before()
		}
		if join.done == nil {
			continue
		}
		timer := time.NewTimer(timeout)
		select {
		case <-join.done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			diagnostics = append(diagnostics, fmt.Sprintf("%s did not join", join.name))
		}
	}
	return diagnostics
}

func reportCleanupDiagnostics(t *testing.T, diagnostics []string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		t.Errorf("cleanup: %s", diagnostic)
	}
}

type trackedApply struct {
	result <-chan applyOutcome
	done   <-chan struct{}
}

func startTrackedApply(actor *Actor, ctx context.Context, proposal ProposedTransition) trackedApply {
	result := make(chan applyOutcome, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		transition, err := actor.Apply(ctx, proposal)
		result <- applyOutcome{result: transition, err: err}
	}()
	return trackedApply{result: result, done: done}
}

func startTrackedClose(actor *Actor) (<-chan error, <-chan struct{}) {
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- actor.Close()
	}()
	return result, done
}

func initializeTrackedActor(t *testing.T) (*Actor, *controlledGenerator) {
	t.Helper()
	generator := newControlledGenerator()
	actor := New(context.Background(), generator.Generate)
	initial := startTrackedApply(actor, context.Background(), testProposal(
		0, testState("/start", protocol.ModeInsert, "[I] /start/ "), true, protocol.Effect{},
	))
	call := generator.Next(t)
	call.Complete([]candidate.Record{testRecord("start", "/start")}, nil)
	if outcome := awaitApply(t, initial.result); outcome.err != nil {
		t.Fatalf("initialize Apply() = %v", outcome.err)
	}
	select {
	case <-initial.done:
	case <-time.After(2 * time.Second):
		t.Fatal("initial public Apply wrapper did not join")
	}
	return actor, generator
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

func TestActorCleanupAttemptsLaterJoinsAfterActorTimeout(t *testing.T) {
	actorDone := make(chan struct{})
	laterDone := make(chan struct{})
	laterAttempted := false

	diagnostics := runActorCleanup(nil, []cleanupJoin{
		{name: "actor shutdown", done: actorDone},
		{
			name: "later wrapper",
			done: laterDone,
			before: func() {
				laterAttempted = true
				close(laterDone)
			},
		},
	}, time.Millisecond)
	close(actorDone)

	if !laterAttempted {
		t.Fatal("cleanup stopped before later join")
	}
	if !slices.Equal(diagnostics, []string{"actor shutdown did not join"}) {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}
