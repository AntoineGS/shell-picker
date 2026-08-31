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
	nextID    uint64
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

func (actor *Actor) run(sessionCtx context.Context, generate GenerateFunc) {
	defer close(actor.done)
	current := Snapshot{records: ownRecordSet(nil)}
	var pending *pendingTransition
	var replacement *applyCommand
	var closeReply chan error
	var shutdownErr error
	sessionDone := sessionCtx.Done()
	replyApplyError := func(command *applyCommand, err error) {
		err = errors.Join(err, actor.cleanup(command.proposal.Created))
		command.reply <- applyReply{err: err}
	}
	replyEnrichError := func(command *enrichCommand, err error) {
		command.reply <- enrichReply{err: err}
	}
	nextGeneration := func() (uint64, bool) {
		if actor.nextID == math.MaxUint64 {
			return 0, false
		}
		actor.nextID++
		return actor.nextID, true
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
		generation, ok := nextGeneration()
		if !ok {
			replyApplyError(command, errGenerationLimit)
			return nil
		}
		buildCtx, cancel := context.WithCancelCause(sessionCtx)
		transition := &pendingTransition{id: generation, command: command, accepted: accepted, cancel: cancel}
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
			case *enrichCommand:
				accepted := time.Now()
				if shutdownErr != nil {
					replyEnrichError(command, shutdownErr)
				} else if cause := context.Cause(command.ctx); cause != nil {
					replyEnrichError(command, cause)
				} else if cause := context.Cause(sessionCtx); cause != nil {
					replyEnrichError(command, cause)
				} else if command.baseGeneration != current.generation {
					replyEnrichError(command, ErrStaleGeneration)
				} else if pending != nil {
					replyEnrichError(command, ErrTransitionPending)
				} else {
					generation, ok := nextGeneration()
					if !ok {
						replyEnrichError(command, errGenerationLimit)
						continue
					}
					current = Snapshot{
						generation: generation,
						state:      cloneState(current.state),
						records:    ownRecordSet(command.records),
					}
					result := enrichTransitionResult(current, command, accepted)
					command.reply <- enrichReply{result: result}
				}
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
				if shutdownErr != nil {
					command.reply <- resolveReply{err: shutdownErr}
				} else if cause := context.Cause(command.ctx); cause != nil {
					command.reply <- resolveReply{err: cause}
				} else if record, found := current.lookupRecord(command.key); !found {
					command.reply <- resolveReply{err: ErrUnknownRecord}
				} else {
					command.reply <- resolveReply{record: cloneRecord(record)}
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
					effect := proposal.Effect
					effect.ReloadGeneration = pending.id
					current = Snapshot{
						generation: pending.id,
						state:      cloneState(proposal.State), records: cloneRecordSet(command.result.Records),
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
