package session

import (
	"context"
	"errors"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
)

func (actor *Actor) Apply(ctx context.Context, proposal ProposedTransition) (TransitionResult, error) {
	if ctx == nil {
		return TransitionResult{}, errNilContext
	}
	command := &applyCommand{ctx: ctx, proposal: cloneProposal(proposal), submitted: time.Now(), reply: make(chan applyReply, 1)}
	if err := actor.enqueue(ctx, command); err != nil {
		return TransitionResult{}, errors.Join(err, rollback(command.proposal.Created))
	}
	reply := <-command.reply
	return reply.result, reply.err
}

func (actor *Actor) Enrich(ctx context.Context, baseGeneration uint64, records []candidate.Record, sources candidate.SourceMetrics) (TransitionResult, error) {
	if ctx == nil {
		return TransitionResult{}, errNilContext
	}
	command := &enrichCommand{ctx: ctx, baseGeneration: baseGeneration, records: cloneRecords(records), sources: sources,
		submitted: time.Now(), reply: make(chan enrichReply, 1)}
	if err := actor.enqueue(ctx, command); err != nil {
		return TransitionResult{}, err
	}
	reply := <-command.reply
	return reply.result, reply.err
}

func (actor *Actor) Current(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errNilContext
	}
	command := currentCommand{ctx: ctx, reply: make(chan snapshotReply, 1)}
	if err := actor.enqueue(ctx, command); err != nil {
		return Snapshot{}, err
	}
	reply := <-command.reply
	return reply.snapshot, reply.err
}

func (actor *Actor) CurrentState(ctx context.Context) (State, error) {
	if ctx == nil {
		return State{}, errNilContext
	}
	command := currentStateCommand{ctx: ctx, reply: make(chan stateReply, 1)}
	if err := actor.enqueue(ctx, command); err != nil {
		return State{}, err
	}
	reply := <-command.reply
	return reply.state, reply.err
}

func (actor *Actor) Snapshot(ctx context.Context, generation uint64) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errNilContext
	}
	command := snapshotCommand{ctx: ctx, generation: generation, reply: make(chan snapshotReply, 1)}
	if err := actor.enqueue(ctx, command); err != nil {
		return Snapshot{}, err
	}
	reply := <-command.reply
	return reply.snapshot, reply.err
}

func (actor *Actor) ResolveCurrent(ctx context.Context, raw []byte) (candidate.Record, error) {
	if ctx == nil {
		return candidate.Record{}, errNilContext
	}
	command := resolveCommand{ctx: ctx, key: string(raw), reply: make(chan resolveReply, 1)}
	if err := actor.enqueue(ctx, command); err != nil {
		return candidate.Record{}, err
	}
	reply := <-command.reply
	return reply.record, reply.err
}

func (actor *Actor) Close() error {
	actor.closeOnce.Do(func() {
		reply := make(chan error, 1)
		select {
		case actor.commands <- closeCommand{reply: reply}:
			actor.closeErr = <-reply
		case <-actor.done:
		}
		close(actor.closeWait)
	})
	<-actor.closeWait
	return actor.closeErr
}

func (actor *Actor) enqueue(ctx context.Context, command any) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	select {
	case <-actor.done:
		return actor.terminalError()
	default:
	}
	select {
	case actor.commands <- command:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-actor.done:
		return actor.terminalError()
	}
}

func (actor *Actor) terminalError() error {
	if actor.finalErr != nil {
		return actor.finalErr
	}
	return ErrClosed
}
