package session

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type contentionReply struct {
	id      int
	outcome applyOutcome
}

func cardinalityApply(actor *Actor, ctx context.Context, proposal ProposedTransition) *applyCommand {
	command := &applyCommand{
		ctx: ctx, proposal: cloneProposal(proposal), submitted: time.Now(), reply: make(chan applyReply, 2),
	}
	actor.commands <- command
	return command
}

func exactlyOneReply[T any](t *testing.T, name string, replies <-chan T) T {
	t.Helper()
	var reply T
	select {
	case reply = <-replies:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s received no reply", name)
	}
	select {
	case duplicate := <-replies:
		t.Fatalf("%s received duplicate reply: %+v", name, duplicate)
	default:
	}
	return reply
}

func TestActorHighContentionRepliesOnceAndClosesWithoutStalePublication(t *testing.T) {
	actor, generator := initializeActor(t)
	blockedProposal := testProposal(
		1, testState("/blocked", protocol.ModeNormal, "blocked"), true,
		protocol.Effect{Accept: true, ClearQuery: true},
	)
	blocked := asyncApply(actor, context.Background(), blockedProposal)
	blockedCall := generator.Next(t)

	replacement := asyncApply(actor, context.Background(), testProposal(
		1, testState("/replacement", protocol.ModeNormal, "replacement"), true,
		protocol.Effect{ClearMulti: true},
	))
	select {
	case <-blockedCall.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("designated replacement did not retire blocked generation")
	}
	current, err := actor.Current(context.Background())
	if err != nil || current.Generation() != 1 || current.State().Prompt != "[I] /start/ " || current.Records()[0].Display != "start" {
		t.Fatalf("retiring generation published stale state: %+v, %v", current, err)
	}

	const excess = 64
	start := make(chan struct{})
	replies := make(chan contentionReply, excess)
	cancels := make([]context.CancelFunc, excess)
	var callers sync.WaitGroup
	callers.Add(excess)
	for id := range excess {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[id] = cancel
		go func() {
			defer callers.Done()
			<-start
			result, err := actor.Apply(ctx, testProposal(
				1, testState("/excess", protocol.ModeNormal, "excess"), true, protocol.Effect{Accept: true},
			))
			replies <- contentionReply{id: id, outcome: applyOutcome{result: result, err: err}}
		}()
	}
	close(start)
	for id := 0; id < excess; id += 2 {
		cancels[id]()
	}
	closed := make(chan error, 1)
	go func() { closed <- actor.Close() }()

	seen := make([]bool, excess)
	for range excess {
		select {
		case reply := <-replies:
			if seen[reply.id] {
				t.Fatalf("Apply %d replied more than once", reply.id)
			}
			seen[reply.id] = true
			if errors.Is(reply.outcome.err, ErrTransitionPending) {
				// Rejected while the designated replacement occupied the only queue slot.
			} else if !errors.Is(reply.outcome.err, context.Canceled) && !errors.Is(reply.outcome.err, ErrClosed) {
				t.Fatalf("Apply %d error = %v", reply.id, reply.outcome.err)
			}
			if reply.outcome.result.Snapshot.Generation() != 0 || reply.outcome.result.Effect != (protocol.Effect{}) {
				t.Fatalf("rejected Apply %d leaked result: %+v", reply.id, reply.outcome.result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("excess Apply did not receive a bounded reply")
		}
	}
	joined := make(chan struct{})
	go func() {
		callers.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("Apply caller goroutines did not exit")
	}
	select {
	case duplicate := <-replies:
		t.Fatalf("unexpected duplicate reply: %+v", duplicate)
	default:
	}

	closeAccepted := make(chan error, 1)
	go func() {
		for {
			_, currentErr := actor.Current(context.Background())
			if errors.Is(currentErr, ErrClosed) {
				closeAccepted <- nil
				return
			}
			if currentErr != nil {
				closeAccepted <- currentErr
				return
			}
			runtime.Gosched()
		}
	}()
	select {
	case err := <-closeAccepted:
		if err != nil {
			t.Fatalf("observe Close acceptance: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close was not accepted while generation retired")
	}

	blockedCall.Complete([]candidate.Record{testRecord("stale", "/stale")}, nil)
	blockedOutcome := awaitApply(t, blocked)
	if !errors.Is(blockedOutcome.err, ErrSuperseded) || blockedOutcome.result.Snapshot.Generation() != 0 ||
		blockedOutcome.result.Effect != (protocol.Effect{}) {
		t.Fatalf("blocked Apply = %+v", blockedOutcome)
	}
	replacementOutcome := awaitApply(t, replacement)
	if !errors.Is(replacementOutcome.err, ErrClosed) || replacementOutcome.result.Snapshot.Generation() != 0 ||
		replacementOutcome.result.Effect != (protocol.Effect{}) {
		t.Fatalf("replacement Apply = %+v", replacementOutcome)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not complete")
	}
	select {
	case call := <-generator.started:
		t.Fatalf("more than one replacement started: %q", call.request.Location.Path)
	default:
	}
	select {
	case <-actor.done:
	default:
		t.Fatal("actor goroutine leaked after Close")
	}
}

func TestActorPrivateCommandsReplyExactlyOnceUnderContentionAndClose(t *testing.T) {
	generator := newControlledGenerator()
	actor := New(context.Background(), generator.Generate)
	blocked := cardinalityApply(actor, context.Background(), testProposal(
		0, testState("/blocked", protocol.ModeNormal, "blocked"), true, protocol.Effect{Accept: true},
	))
	blockedCall := generator.Next(t)
	replacement := cardinalityApply(actor, context.Background(), testProposal(
		0, testState("/replacement", protocol.ModeNormal, "replacement"), true, protocol.Effect{ClearMulti: true},
	))
	select {
	case <-blockedCall.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not retire blocked generation")
	}

	const excess = 64
	commands := make([]*applyCommand, excess)
	accepted := make(chan int, excess)
	start := make(chan struct{})
	for id := range excess {
		ctx, cancel := context.WithCancel(context.Background())
		if id%2 == 0 {
			cancel()
		} else {
			defer cancel()
		}
		commands[id] = &applyCommand{
			ctx: ctx,
			proposal: cloneProposal(testProposal(
				0, testState("/excess", protocol.ModeNormal, "excess"), true, protocol.Effect{Accept: true},
			)),
			submitted: time.Now(),
			reply:     make(chan applyReply, 2),
		}
		go func() {
			<-start
			actor.commands <- commands[id]
			accepted <- id
		}()
	}
	close(start)
	seenAccepted := make([]bool, excess)
	for range excess {
		select {
		case id := <-accepted:
			if seenAccepted[id] {
				t.Fatalf("command %d accepted twice", id)
			}
			seenAccepted[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("private Apply command was not accepted")
		}
	}

	current := currentCommand{ctx: context.Background(), reply: make(chan snapshotReply, 2)}
	actor.commands <- current
	snapshot := snapshotCommand{ctx: context.Background(), generation: 0, reply: make(chan snapshotReply, 2)}
	actor.commands <- snapshot
	resolve := resolveCommand{ctx: context.Background(), key: "forged", reply: make(chan resolveReply, 2)}
	actor.commands <- resolve
	closeRequest := closeCommand{reply: make(chan error, 2)}
	actor.commands <- closeRequest
	blockedCall.Complete([]candidate.Record{testRecord("stale", "/stale")}, nil)
	select {
	case <-actor.done:
	case <-time.After(2 * time.Second):
		t.Fatal("actor did not exit after completion and Close")
	}

	blockedReply := exactlyOneReply(t, "blocked Apply", blocked.reply)
	if !errors.Is(blockedReply.err, ErrSuperseded) {
		t.Fatalf("blocked Apply = %v", blockedReply.err)
	}
	replacementReply := exactlyOneReply(t, "replacement Apply", replacement.reply)
	if !errors.Is(replacementReply.err, ErrClosed) {
		t.Fatalf("replacement Apply = %v", replacementReply.err)
	}
	for id, command := range commands {
		reply := exactlyOneReply(t, "excess Apply", command.reply)
		if id%2 == 0 {
			if !errors.Is(reply.err, context.Canceled) {
				t.Fatalf("canceled Apply %d = %v", id, reply.err)
			}
		} else if !errors.Is(reply.err, ErrTransitionPending) {
			t.Fatalf("excess Apply %d = %v", id, reply.err)
		}
	}
	currentReply := exactlyOneReply(t, "Current", current.reply)
	if currentReply.err != nil || currentReply.snapshot.Generation() != 0 {
		t.Fatalf("Current = %+v", currentReply)
	}
	snapshotReply := exactlyOneReply(t, "Snapshot", snapshot.reply)
	if snapshotReply.err != nil || snapshotReply.snapshot.Generation() != 0 {
		t.Fatalf("Snapshot = %+v", snapshotReply)
	}
	resolveReply := exactlyOneReply(t, "ResolveCurrent", resolve.reply)
	if !errors.Is(resolveReply.err, ErrUnknownRecord) {
		t.Fatalf("ResolveCurrent = %+v", resolveReply)
	}
	if err := exactlyOneReply(t, "Close", closeRequest.reply); err != nil {
		t.Fatalf("Close = %v", err)
	}
	select {
	case call := <-generator.started:
		t.Fatalf("queued replacement started: %q", call.request.Location.Path)
	default:
	}
	if err := actor.Close(); err != nil {
		t.Fatalf("idempotent Close = %v", err)
	}
}
