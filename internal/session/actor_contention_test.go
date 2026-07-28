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

func exactlyOneCollectedReply[T any](t *testing.T, name string, collector *replyCollector[T]) T {
	t.Helper()
	replies := collector.StopAndReplies(t)
	if len(replies) != 1 {
		t.Fatalf("%s reply count = %d; want 1; replies=%+v", name, len(replies), replies)
	}
	return replies[0]
}

func TestActorHighContentionRepliesOnceAndClosesWithoutStalePublication(t *testing.T) {
	actor, generator := initializeTrackedActor(t)
	blockedProposal := testProposal(
		1, testState("/blocked", protocol.ModeNormal, "blocked"), true,
		protocol.Effect{Accept: true, ClearQuery: true},
	)
	blocked := startTrackedApply(actor, context.Background(), blockedProposal)
	blockedCall := generator.Next(t)
	startGate := newOnceGate()
	var completion sync.Once
	var callers sync.WaitGroup
	var cancels []context.CancelFunc
	var joins []cleanupJoin
	joins = append(joins, cleanupJoin{name: "original public Apply wrapper", done: blocked.done})
	finishGeneration := func() {
		completion.Do(func() { blockedCall.Complete(nil, context.Canceled) })
	}
	t.Cleanup(func() {
		actions := []func(){
			func() {
				for _, cancel := range cancels {
					cancel()
				}
			},
			startGate.Open,
			finishGeneration,
		}
		for _, action := range actions {
			action()
		}
		_, cleanupCloseDone := startTrackedClose(actor)
		cleanupJoins := []cleanupJoin{{name: "actor shutdown", done: actor.done}, {name: "cleanup Close wrapper", done: cleanupCloseDone}}
		cleanupJoins = append(cleanupJoins, joins...)
		reportCleanupDiagnostics(t, runActorCleanup(nil, cleanupJoins, 2*time.Second))
	})

	replacement := startTrackedApply(actor, context.Background(), testProposal(
		1, testState("/replacement", protocol.ModeNormal, "replacement"), true,
		protocol.Effect{ClearMulti: true},
	))
	joins = append(joins, cleanupJoin{name: "replacement public Apply wrapper", done: replacement.done})
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
	replies := make(chan contentionReply, excess)
	cancels = make([]context.CancelFunc, excess)
	callers.Add(excess)
	for id := range excess {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[id] = cancel
		go func() {
			defer callers.Done()
			<-startGate.channel
			result, err := actor.Apply(ctx, testProposal(
				1, testState("/excess", protocol.ModeNormal, "excess"), true, protocol.Effect{Accept: true},
			))
			replies <- contentionReply{id: id, outcome: applyOutcome{result: result, err: err}}
		}()
	}
	callersDone := make(chan struct{})
	go func() {
		callers.Wait()
		close(callersDone)
	}()
	joins = append(joins, cleanupJoin{name: "excess public Apply wrappers", done: callersDone})
	startGate.Open()
	for id := 0; id < excess; id += 2 {
		cancels[id]()
	}
	closed := make(chan error, 1)
	closeWrapperDone := make(chan struct{})
	go func() {
		defer close(closeWrapperDone)
		closed <- actor.Close()
	}()
	joins = append(joins, cleanupJoin{name: "contention Close wrapper", done: closeWrapperDone})

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
	select {
	case <-callersDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Apply caller goroutines did not exit")
	}
	select {
	case duplicate := <-replies:
		t.Fatalf("unexpected duplicate reply: %+v", duplicate)
	default:
	}

	closeAccepted := make(chan error, 1)
	closeObserverDone := make(chan struct{})
	go func() {
		defer close(closeObserverDone)
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
	joins = append(joins, cleanupJoin{name: "Close acceptance observer", done: closeObserverDone})
	select {
	case err := <-closeAccepted:
		if err != nil {
			t.Fatalf("observe Close acceptance: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close was not accepted while generation retired")
	}

	completion.Do(func() { blockedCall.Complete([]candidate.Record{testRecord("stale", "/stale")}, nil) })
	blockedOutcome := awaitApply(t, blocked.result)
	if !errors.Is(blockedOutcome.err, ErrSuperseded) || blockedOutcome.result.Snapshot.Generation() != 0 ||
		blockedOutcome.result.Effect != (protocol.Effect{}) {
		t.Fatalf("blocked Apply = %+v", blockedOutcome)
	}
	replacementOutcome := awaitApply(t, replacement.result)
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
	case <-time.After(2 * time.Second):
		t.Fatal("actor goroutine did not exit after Close")
	}
}

func TestActorPrivateCommandsReplyExactlyOnceUnderContentionAndClose(t *testing.T) {
	generator := newControlledGenerator()
	actor := New(context.Background(), generator.Generate)
	var submitters sync.WaitGroup
	var completion sync.Once
	var blockedCall *generatorCall
	var commandCancels []context.CancelFunc
	var applyCollectors []*replyCollector[applyReply]
	var currentCollector *replyCollector[snapshotReply]
	var snapshotCollector *replyCollector[snapshotReply]
	var resolveCollector *replyCollector[resolveReply]
	var closeCollector *replyCollector[error]
	submissionGate := newOnceGate()
	t.Cleanup(func() {
		actions := []func(){
			submissionGate.Open,
			func() {
				for _, cancel := range commandCancels {
					cancel()
				}
			},
			func() {
				if blockedCall != nil {
					completion.Do(func() { blockedCall.Complete(nil, context.Canceled) })
				}
			},
		}
		for _, action := range actions {
			action()
		}
		_, closeDone := startTrackedClose(actor)
		submittersDone := make(chan struct{})
		go func() {
			submitters.Wait()
			close(submittersDone)
		}()
		joins := []cleanupJoin{
			{name: "actor shutdown", done: actor.done},
			{name: "cleanup Close wrapper", done: closeDone},
			{name: "command submitters", done: submittersDone},
		}
		for _, collector := range applyCollectors {
			joins = append(joins, cleanupJoin{name: "Apply reply collector", done: collector.done, before: collector.stop.Open})
		}
		if currentCollector != nil {
			joins = append(joins, cleanupJoin{name: "Current reply collector", done: currentCollector.done, before: currentCollector.stop.Open})
		}
		if snapshotCollector != nil {
			joins = append(joins, cleanupJoin{name: "Snapshot reply collector", done: snapshotCollector.done, before: snapshotCollector.stop.Open})
		}
		if resolveCollector != nil {
			joins = append(joins, cleanupJoin{name: "ResolveCurrent reply collector", done: resolveCollector.done, before: resolveCollector.stop.Open})
		}
		if closeCollector != nil {
			joins = append(joins, cleanupJoin{name: "Close reply collector", done: closeCollector.done, before: closeCollector.stop.Open})
		}
		reportCleanupDiagnostics(t, runActorCleanup(nil, joins, 2*time.Second))
	})
	submit := func(command any) {
		t.Helper()
		accepted := make(chan bool, 1)
		submitters.Add(1)
		go func() {
			defer submitters.Done()
			select {
			case actor.commands <- command:
				accepted <- true
			case <-actor.done:
				accepted <- false
			}
		}()
		select {
		case ok := <-accepted:
			if !ok {
				t.Fatal("actor closed before private command submission")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("private command submission blocked")
		}
	}
	newApply := func(ctx context.Context, proposal ProposedTransition) *applyCommand {
		collector := newReplyCollector[applyReply]()
		applyCollectors = append(applyCollectors, collector)
		return &applyCommand{
			ctx: ctx, proposal: cloneProposal(proposal), submitted: time.Now(), reply: collector.input,
		}
	}
	blocked := newApply(context.Background(), testProposal(
		0, testState("/blocked", protocol.ModeNormal, "blocked"), true, protocol.Effect{Accept: true},
	))
	submit(blocked)
	blockedCall = generator.Next(t)
	replacement := newApply(context.Background(), testProposal(
		0, testState("/replacement", protocol.ModeNormal, "replacement"), true, protocol.Effect{ClearMulti: true},
	))
	submit(replacement)
	select {
	case <-blockedCall.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not retire blocked generation")
	}

	const excess = 64
	commands := make([]*applyCommand, excess)
	accepted := make(chan int, excess)
	for id := range excess {
		ctx, cancel := context.WithCancel(context.Background())
		commandCancels = append(commandCancels, cancel)
		if id%2 == 0 {
			cancel()
		}
		commands[id] = newApply(ctx, testProposal(
			0, testState("/excess", protocol.ModeNormal, "excess"), true, protocol.Effect{Accept: true},
		))
		submitters.Add(1)
		go func() {
			defer submitters.Done()
			<-submissionGate.channel
			select {
			case actor.commands <- commands[id]:
				accepted <- id
			case <-actor.done:
			}
		}()
	}
	submissionGate.Open()
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

	currentCollector = newReplyCollector[snapshotReply]()
	current := currentCommand{ctx: context.Background(), reply: currentCollector.input}
	submit(current)
	snapshotCollector = newReplyCollector[snapshotReply]()
	snapshot := snapshotCommand{ctx: context.Background(), generation: 0, reply: snapshotCollector.input}
	submit(snapshot)
	resolveCollector = newReplyCollector[resolveReply]()
	resolve := resolveCommand{ctx: context.Background(), key: "forged", reply: resolveCollector.input}
	submit(resolve)
	closeCollector = newReplyCollector[error]()
	closeRequest := closeCommand{reply: closeCollector.input}
	submit(closeRequest)
	completion.Do(func() { blockedCall.Complete([]candidate.Record{testRecord("stale", "/stale")}, nil) })
	select {
	case <-actor.done:
	case <-time.After(2 * time.Second):
		t.Fatal("actor did not exit after completion and Close")
	}

	blockedReply := exactlyOneCollectedReply(t, "blocked Apply", applyCollectors[0])
	if !errors.Is(blockedReply.err, ErrSuperseded) {
		t.Fatalf("blocked Apply = %v", blockedReply.err)
	}
	replacementReply := exactlyOneCollectedReply(t, "replacement Apply", applyCollectors[1])
	if !errors.Is(replacementReply.err, ErrClosed) {
		t.Fatalf("replacement Apply = %v", replacementReply.err)
	}
	for id := range commands {
		reply := exactlyOneCollectedReply(t, "excess Apply", applyCollectors[id+2])
		if id%2 == 0 {
			if !errors.Is(reply.err, context.Canceled) {
				t.Fatalf("canceled Apply %d = %v", id, reply.err)
			}
		} else if !errors.Is(reply.err, ErrTransitionPending) {
			t.Fatalf("excess Apply %d = %v", id, reply.err)
		}
	}
	currentReply := exactlyOneCollectedReply(t, "Current", currentCollector)
	if currentReply.err != nil || currentReply.snapshot.Generation() != 0 {
		t.Fatalf("Current = %+v", currentReply)
	}
	snapshotReply := exactlyOneCollectedReply(t, "Snapshot", snapshotCollector)
	if snapshotReply.err != nil || snapshotReply.snapshot.Generation() != 0 {
		t.Fatalf("Snapshot = %+v", snapshotReply)
	}
	resolveReply := exactlyOneCollectedReply(t, "ResolveCurrent", resolveCollector)
	if !errors.Is(resolveReply.err, ErrUnknownRecord) {
		t.Fatalf("ResolveCurrent = %+v", resolveReply)
	}
	if err := exactlyOneCollectedReply(t, "Close", closeCollector); err != nil {
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
