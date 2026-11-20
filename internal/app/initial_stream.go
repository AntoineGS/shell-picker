package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type initialIndexInput struct {
	reader  *io.PipeReader
	writer  *io.PipeWriter
	barrier initialStreamGate
	run     *initialStreamRun
	cancel  context.CancelCauseFunc
}

func newInitialIndexInput(picker protocol.Picker, customLaunch bool, input *io.ReadCloser) *initialIndexInput {
	if picker != protocol.PickerSearch || !supportsInitialIndexedStream() || customLaunch {
		return nil
	}
	reader, writer := io.Pipe()
	*input = reader
	return &initialIndexInput{reader: reader, writer: writer}
}

func (input *initialIndexInput) gate() *initialStreamGate {
	if input == nil {
		return nil
	}
	return &input.barrier
}

func (input *initialIndexInput) start(ctx context.Context, backend *pickerBackend, base uint64, request candidate.BuildRequest) context.Context {
	if input == nil {
		return ctx
	}
	launchCtx, cancel := context.WithCancelCause(ctx)
	input.cancel = cancel
	input.run = startInitialStream(ctx, input.writer, func(ctx context.Context, writer io.Writer) error {
		backend.trace.event(integrationpkg.TraceEvent{Name: "generation.start", Generation: base + 1, Outcome: "ok"})
		database, records, err := backend.indexed.streamInitial(ctx, request, writer)
		if err != nil {
			backend.trace.event(integrationpkg.TraceEvent{Name: "generation.discard", Generation: base + 1, Outcome: traceDiscardOutcome(err)})
			// fzf can close the read side before returning an ordinary abort.
			if !errors.Is(err, io.ErrClosedPipe) {
				cancel(err)
			}
			return err
		}
		final, err := backend.actor.Enrich(ctx, base, records, candidate.SourceMetrics{})
		if err != nil {
			cancel(err)
			return fmt.Errorf("install initial indexed generation: %w", err)
		}
		backend.indexed.cache(database)
		traceInitialTransition(backend.trace, backend.metrics.policy, request.Picker, final, request.Location.Path)
		backend.metrics.recordTransition(final)
		input.barrier.markActorReady()
		return nil
	})
	return launchCtx
}

func (input *initialIndexInput) finish(ctx context.Context, result fzf.Result, launchErr error) error {
	if input == nil {
		return nil
	}
	ready := input.barrier.ready()
	if launchErr != nil || result.Aborted || !ready {
		input.run.stop(context.Canceled)
	}
	err := input.run.wait()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil && !errors.Is(err, context.Canceled) && !(result.Aborted && errors.Is(err, io.ErrClosedPipe)) {
		return fmt.Errorf("load initial indexed candidates: %w", err)
	}
	if launchErr == nil && !result.Aborted && !ready {
		return errors.New("fzf exited before the initial index was ready")
	}
	return nil
}

func (input *initialIndexInput) close(cause error) {
	if input == nil {
		return
	}
	if input.run != nil {
		input.run.stop(cause)
		_ = input.run.wait()
	}
	_ = input.reader.Close()
	_ = input.writer.Close()
	if input.cancel != nil {
		input.cancel(cause)
	}
}

type initialStreamRun struct {
	cancel   context.CancelCauseFunc
	writer   *io.PipeWriter
	done     chan error
	once     sync.Once
	waitOnce sync.Once
	result   error
}

func startInitialStream(parent context.Context, writer *io.PipeWriter, run func(context.Context, io.Writer) error) *initialStreamRun {
	ctx, cancel := context.WithCancelCause(parent)
	stream := &initialStreamRun{cancel: cancel, writer: writer, done: make(chan error, 1)}
	go func() {
		err := run(ctx, writer)
		if err != nil {
			_ = writer.CloseWithError(err)
		} else {
			_ = writer.Close()
		}
		stream.done <- err
	}()
	return stream
}

func (stream *initialStreamRun) stop(cause error) {
	stream.once.Do(func() {
		if cause != nil {
			stream.cancel(cause)
			_ = stream.writer.CloseWithError(cause)
		}
	})
}

func (stream *initialStreamRun) wait() error {
	stream.waitOnce.Do(func() {
		stream.result = <-stream.done
		stream.cancel(stream.result)
	})
	return stream.result
}
