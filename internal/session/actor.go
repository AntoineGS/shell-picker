package session

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
)

var (
	ErrClosed            = errors.New("session actor is closed")
	ErrSuperseded        = errors.New("session transition was superseded")
	ErrStaleGeneration   = errors.New("session generation is stale")
	ErrTransitionPending = errors.New("session transition is already pending")
	ErrUnknownRecord     = errors.New("record is not in the current snapshot")
	errNilContext        = errors.New("session actor: nil context")
	errNilGenerator      = errors.New("session actor: nil generator")
	errGenerationLimit   = errors.New("session generation limit reached")
)

type Actor struct {
	commands  chan any
	done      chan struct{}
	closeOnce sync.Once
	closeWait chan struct{}
	closeErr  error
	finalErr  error
	cleanup   cleanupFunc
}

func New(ctx context.Context, generate GenerateFunc) *Actor {
	return newActor(ctx, generate, rollback)
}

func newActor(ctx context.Context, generate GenerateFunc, cleanup cleanupFunc) *Actor {
	if ctx == nil {
		ctx = context.Background()
	}
	actor := &Actor{
		commands:  make(chan any),
		done:      make(chan struct{}),
		closeWait: make(chan struct{}),
		cleanup:   cleanup,
	}
	go actor.run(ctx, generate)
	return actor
}

func (actor *Actor) Apply(ctx context.Context, proposal ProposedTransition) (TransitionResult, error) {
	if ctx == nil {
		return TransitionResult{}, errNilContext
	}
	command := &applyCommand{
		ctx: ctx, proposal: cloneProposal(proposal), submitted: time.Now(), reply: make(chan applyReply, 1),
	}
	if err := actor.enqueue(ctx, command); err != nil {
		return TransitionResult{}, errors.Join(err, rollback(command.proposal.Created))
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

func (actor *Actor) run(sessionCtx context.Context, generate GenerateFunc) {
	defer close(actor.done)
	current := Snapshot{byFullRecord: make(map[string][]int)}
	var pending *pendingTransition
	var replacement *applyCommand
	var closeReply chan error
	var shutdownErr error
	var nextID uint64
	sessionDone := sessionCtx.Done()
	replyApplyError := func(command *applyCommand, err error) {
		err = errors.Join(err, actor.cleanup(command.proposal.Created))
		command.reply <- applyReply{err: err}
	}

	start := func(command *applyCommand) *pendingTransition {
		accepted := time.Now()
		if cause := context.Cause(command.ctx); cause != nil {
			replyApplyError(command, cause)
			return nil
		}
		if command.proposal.BaseGeneration != current.generation {
			replyApplyError(command, ErrStaleGeneration)
			return nil
		}
		if command.proposal.Build == nil {
			current.state = cloneState(command.proposal.State)
			result := transitionResult(current, command, accepted, command.proposal.Effect, candidate.SourceMetrics{})
			command.reply <- applyReply{result: result}
			return nil
		}
		if generate == nil {
			replyApplyError(command, errNilGenerator)
			return nil
		}
		if nextID == math.MaxUint64 {
			replyApplyError(command, errGenerationLimit)
			return nil
		}
		nextID++
		buildCtx, cancel := context.WithCancelCause(sessionCtx)
		transition := &pendingTransition{id: nextID, command: command, accepted: accepted, cancel: cancel}
		request := *command.proposal.Build
		request.Generation = transition.id
		go func(id uint64) {
			result, err := generate(buildCtx, request)
			actor.commands <- completionCommand{id: id, result: result, err: err}
		}(transition.id)
		return transition
	}

	for {
		if pending == nil && shutdownErr != nil {
			if replacement != nil {
				replyApplyError(replacement, shutdownErr)
				replacement = nil
			}
			actor.finalErr = shutdownErr
			if closeReply != nil {
				closeReply <- nil
			}
			return
		}

		var callerDone <-chan struct{}
		if pending != nil && !pending.retiring {
			callerDone = pending.command.ctx.Done()
		}

		select {
		case raw := <-actor.commands:
			switch command := raw.(type) {
			case *applyCommand:
				if shutdownErr != nil {
					replyApplyError(command, shutdownErr)
					continue
				}
				if cause := context.Cause(command.ctx); cause != nil {
					replyApplyError(command, cause)
					continue
				}
				if command.proposal.BaseGeneration != current.generation {
					replyApplyError(command, ErrStaleGeneration)
					continue
				}
				if pending == nil {
					pending = start(command)
					continue
				}
				if pending.retiring || replacement != nil {
					replyApplyError(command, ErrTransitionPending)
					continue
				}
				pending.retiring = true
				pending.replyErr = ErrSuperseded
				pending.cancel(ErrSuperseded)
				replacement = command
			case currentCommand:
				if shutdownErr != nil {
					command.reply <- snapshotReply{err: shutdownErr}
				} else if cause := context.Cause(command.ctx); cause != nil {
					command.reply <- snapshotReply{err: cause}
				} else {
					command.reply <- snapshotReply{snapshot: cloneSnapshot(current)}
				}
			case currentStateCommand:
				if shutdownErr != nil {
					command.reply <- stateReply{err: shutdownErr}
				} else if cause := context.Cause(command.ctx); cause != nil {
					command.reply <- stateReply{err: cause}
				} else {
					command.reply <- stateReply{state: cloneState(current.state)}
				}
			case snapshotCommand:
				if shutdownErr != nil {
					command.reply <- snapshotReply{err: shutdownErr}
				} else if cause := context.Cause(command.ctx); cause != nil {
					command.reply <- snapshotReply{err: cause}
				} else if command.generation != current.generation {
					command.reply <- snapshotReply{err: ErrStaleGeneration}
				} else {
					command.reply <- snapshotReply{snapshot: cloneSnapshot(current)}
				}
			case resolveCommand:
				positions := current.byFullRecord[command.key]
				if shutdownErr != nil {
					command.reply <- resolveReply{err: shutdownErr}
				} else if cause := context.Cause(command.ctx); cause != nil {
					command.reply <- resolveReply{err: cause}
				} else if len(positions) == 0 {
					command.reply <- resolveReply{err: ErrUnknownRecord}
				} else {
					command.reply <- resolveReply{record: cloneRecord(current.records[positions[0]])}
				}
			case completionCommand:
				if pending == nil || command.id != pending.id {
					continue
				}
				pending.cancel(nil)
				proposal := pending.command.proposal
				if pending.retiring {
					replyApplyError(pending.command, pending.replyErr)
				} else if cause := context.Cause(pending.command.ctx); cause != nil {
					replyApplyError(pending.command, cause)
				} else if cause := context.Cause(sessionCtx); cause != nil {
					replyApplyError(pending.command, cause)
				} else if command.err != nil {
					replyApplyError(pending.command, command.err)
				} else if proposal.BaseGeneration != current.generation {
					replyApplyError(pending.command, ErrStaleGeneration)
				} else {
					records := cloneRecords(command.result.Records)
					effect := proposal.Effect
					effect.ReloadGeneration = pending.id
					current = Snapshot{
						generation: pending.id,
						state:      cloneState(proposal.State), records: records, byFullRecord: buildIndex(records),
					}
					result := transitionResult(current, pending.command, pending.accepted, effect, command.result.Metrics)
					pending.command.reply <- applyReply{result: result}
				}
				pending = nil
				if replacement != nil && shutdownErr == nil {
					next := replacement
					replacement = nil
					pending = start(next)
				}
			case closeCommand:
				if closeReply != nil {
					command.reply <- nil
					continue
				}
				closeReply = command.reply
				shutdownErr = ErrClosed
				sessionDone = nil
				if pending != nil && !pending.retiring {
					pending.retiring = true
					pending.replyErr = ErrClosed
					pending.cancel(ErrClosed)
				}
			}
		case <-callerDone:
			pending.retiring = true
			pending.replyErr = context.Cause(pending.command.ctx)
			pending.cancel(pending.replyErr)
		case <-sessionDone:
			shutdownErr = context.Cause(sessionCtx)
			sessionDone = nil
			if pending != nil && !pending.retiring {
				pending.retiring = true
				pending.replyErr = shutdownErr
				pending.cancel(shutdownErr)
			}
		}
	}
}
