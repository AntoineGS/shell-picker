package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type orderingEvent struct {
	name   string
	reply  applyReply
	closed error
}

type createdTreeOrdering struct {
	created       *pathutil.CreatedTree
	createdPath   string
	events        chan orderingEvent
	release       chan struct{}
	firstReply    chan applyReply
	rollbackCalls atomic.Int32
}

func newCreatedTreeOrdering(t *testing.T) *createdTreeOrdering {
	t.Helper()
	createdPath := filepath.Join(t.TempDir(), "created")
	if err := os.Mkdir(createdPath, 0o700); err != nil {
		t.Fatal(err)
	}
	return &createdTreeOrdering{
		created: &pathutil.CreatedTree{
			Target:  pathutil.Filesystem([]byte(createdPath)),
			Created: [][]byte{[]byte(createdPath)},
		},
		createdPath: createdPath,
		events:      make(chan orderingEvent, 16),
		release:     make(chan struct{}),
	}
}

func (ordering *createdTreeOrdering) generate(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
	if string(request.Location.Path) == "/replacement" {
		select {
		case reply := <-ordering.firstReply:
			ordering.events <- orderingEvent{name: "original-reply", reply: reply}
		default:
			ordering.events <- orderingEvent{name: "replacement-before-reply"}
		}
		ordering.events <- orderingEvent{name: "replacement-start"}
		return candidate.BuildResult{}, fs.ErrPermission
	}

	if string(request.Location.Path) != string(ordering.created.Target.Path) {
		return candidate.BuildResult{}, errors.New("generator did not receive created target")
	}
	if _, err := os.Stat(string(ordering.created.Target.Path)); err != nil {
		return candidate.BuildResult{}, err
	}
	_ = string(ordering.created.Created[0])
	ordering.events <- orderingEvent{name: "reading-created-data"}
	<-ctx.Done()
	ordering.events <- orderingEvent{name: "cancel-observed"}
	<-ordering.release
	if _, err := os.Stat(string(ordering.created.Target.Path)); err != nil {
		return candidate.BuildResult{}, err
	}
	_ = string(ordering.created.Created[0])
	ordering.events <- orderingEvent{name: "generation-complete"}
	return candidate.BuildResult{}, context.Cause(ctx)
}

func (ordering *createdTreeOrdering) cleanup(created *pathutil.CreatedTree) error {
	if created == nil {
		return nil
	}
	ordering.rollbackCalls.Add(1)
	err := rollback(created)
	if _, statErr := os.Stat(ordering.createdPath); !errors.Is(statErr, fs.ErrNotExist) {
		err = errors.Join(err, errors.New("created tree remains after rollback"))
	}
	ordering.events <- orderingEvent{name: "rollback-complete"}
	return err
}

func (ordering *createdTreeOrdering) next(t *testing.T, want string) orderingEvent {
	t.Helper()
	select {
	case event := <-ordering.events:
		if event.name != want {
			t.Fatalf("event = %q; want %q", event.name, want)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("event %q not observed", want)
		return orderingEvent{}
	}
}

func directApply(actor *Actor, ctx context.Context, proposal ProposedTransition) *applyCommand {
	command := &applyCommand{
		ctx: ctx, proposal: cloneProposal(proposal), submitted: time.Now(), reply: make(chan applyReply, 1),
	}
	actor.commands <- command
	return command
}

func createdProposal(ordering *createdTreeOrdering) ProposedTransition {
	proposal := testProposal(0, testState(ordering.createdPath, protocol.ModeNormal, "discarded"), true, protocol.Effect{
		Accept: true, ClearQuery: true,
	})
	proposal.Created = ordering.created
	return proposal
}

func assertUnpublished(t *testing.T, actor *Actor) {
	t.Helper()
	snapshot, err := actor.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	if snapshot.Generation() != 0 || snapshot.State().Prompt != "" || len(snapshot.Records()) != 0 {
		t.Fatalf("discarded proposal published: %+v", snapshot)
	}
}

func assertDiscardReply(t *testing.T, reply applyReply, want error) {
	t.Helper()
	if !errors.Is(reply.err, want) {
		t.Fatalf("Apply reply = %v; want %v", reply.err, want)
	}
	if reply.result.Snapshot.Generation() != 0 || reply.result.Effect != (protocol.Effect{}) {
		t.Fatalf("discarded result leaked state/effect: %+v", reply.result)
	}
}

func TestActorCreatedTreeDiscardOrdering(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want error
	}{
		{name: "supersede", kind: "supersede", want: ErrSuperseded},
		{name: "caller cancellation", kind: "caller", want: context.Canceled},
		{name: "session cancellation", kind: "session", want: context.Canceled},
		{name: "close", kind: "close", want: ErrClosed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionCtx, stopSession := context.WithCancel(context.Background())
			defer stopSession()
			ordering := newCreatedTreeOrdering(t)
			actor := newActor(sessionCtx, ordering.generate, ordering.cleanup)
			callerCtx, stopCaller := context.WithCancel(context.Background())
			defer stopCaller()
			first := directApply(actor, callerCtx, createdProposal(ordering))
			ordering.firstReply = first.reply
			ordering.next(t, "reading-created-data")
			assertUnpublished(t, actor)

			var replacement *applyCommand
			var closeDone chan error
			switch test.kind {
			case "supersede":
				replacement = directApply(actor, context.Background(), testProposal(
					0, testState("/replacement", protocol.ModeNormal, "replacement"), true, protocol.Effect{},
				))
			case "caller":
				stopCaller()
			case "session":
				stopSession()
			case "close":
				closeDone = make(chan error, 1)
				go func() { closeDone <- actor.Close() }()
			}

			ordering.next(t, "cancel-observed")
			if _, err := os.Stat(ordering.createdPath); err != nil {
				t.Fatalf("rollback raced generator: %v", err)
			}
			if test.kind != "close" && test.kind != "session" {
				assertUnpublished(t, actor)
			}
			close(ordering.release)
			ordering.next(t, "generation-complete")
			ordering.next(t, "rollback-complete")

			if test.kind == "supersede" {
				replyEvent := ordering.next(t, "original-reply")
				assertDiscardReply(t, replyEvent.reply, test.want)
				ordering.next(t, "replacement-start")
				assertDiscardReply(t, <-replacement.reply, fs.ErrPermission)
				assertUnpublished(t, actor)
				if err := actor.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				reply := <-first.reply
				assertDiscardReply(t, reply, test.want)
				if test.kind == "caller" {
					assertUnpublished(t, actor)
					if err := actor.Close(); err != nil {
						t.Fatal(err)
					}
				} else if test.kind == "close" {
					if err := <-closeDone; err != nil {
						t.Fatal(err)
					}
				} else {
					<-actor.done
					if err := actor.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}

			if got := ordering.rollbackCalls.Load(); got != 1 {
				t.Fatalf("rollback calls = %d; want 1", got)
			}
			if _, err := os.Stat(ordering.createdPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("created tree remains: %v", err)
			}
		})
	}
}
