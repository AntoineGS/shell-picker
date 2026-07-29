package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/callback"
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type PickerOptions struct {
	Picker         protocol.Picker
	CWD            []byte
	Home           []byte
	Output         protocol.OutputFormat
	FZFPath        string
	ExecutablePath string
	ZoxidePolicy   candidate.ZoxidePolicy
	ZoxideTimeout  time.Duration
}

type Dependencies struct {
	CandidateBuilder candidate.Builder
	ProcessRunner    process.Runner
	ZoxidePath       string
	Environment      []string
	ForegroundTTY    *os.File
	TTYOut           io.Writer
	TTYErr           io.Writer

	launchFZF func(context.Context, fzf.Config) (fzf.Result, error)
}

func RunPicker(ctx context.Context, options PickerOptions, dependencies Dependencies) (protocol.Outcome, error) {
	if err := validatePickerOptions(ctx, options); err != nil {
		return protocol.Outcome{}, err
	}

	terminal, ownedTerminal, err := pickerTerminal(dependencies.ForegroundTTY)
	if err != nil {
		return protocol.Outcome{}, err
	}
	if ownedTerminal {
		defer terminal.Close()
	}

	builder, err := sessionBuilder(options, dependencies)
	if err != nil {
		return protocol.Outcome{}, err
	}
	sessionCtx, cancelSession := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelSession(nil)
	actor := session.New(sessionCtx, builder.Build)
	actorOpen := true
	defer func() {
		if actorOpen {
			_ = actor.Close()
		}
	}()

	initialState := session.State{
		Picker: options.Picker, Mode: protocol.ModeInsert,
		Location: pathutil.Filesystem(options.CWD), Home: pathutil.Filesystem(options.Home),
		Prompt: "[I] " + pathutil.PromptDisplay(pathutil.Filesystem(options.CWD)) + " ",
	}
	initial, err := actor.Apply(ctx, session.ProposedTransition{
		State: initialState,
		Build: &candidate.BuildRequest{Picker: options.Picker, Location: initialState.Location, Initial: true},
		Effect: protocol.Effect{Mode: protocol.ModeInsert, Prompt: initialState.Prompt, Search: "on",
			Rebind: protocol.ModeInsert, Cursor: protocol.CursorLine},
	})
	if err != nil {
		return protocol.Outcome{}, fmt.Errorf("build initial candidates: %w", err)
	}

	token, err := sessionipc.NewToken()
	if err != nil {
		return protocol.Outcome{}, err
	}
	traceID := make([]byte, 16)
	if _, err := rand.Read(traceID); err != nil {
		return protocol.Outcome{}, errors.New("generate trace session ID")
	}
	metrics := &pickerMetrics{}
	copy(metrics.traceID[:], traceID)
	metrics.recordTransition(initial)
	backend := &pickerBackend{actor: actor, metrics: metrics}
	server, err := sessionipc.Listen(sessionCtx, token, backend)
	if err != nil {
		return protocol.Outcome{}, err
	}
	serverOpen := true
	defer func() {
		if serverOpen {
			_ = server.Close(context.Background())
		}
	}()

	callback.SetCursor(protocol.CursorLine)
	launch := dependencies.launchFZF
	if launch == nil {
		launch = fzf.Run
	}
	result, launchErr := launch(ctx, fzf.Config{
		Picker: options.Picker, FZFPath: options.FZFPath, ExecutablePath: options.ExecutablePath,
		Environment: process.SanitizeEnv(dependencies.Environment, nil), CallbackAddress: server.Address(),
		CallbackToken: token.String(), Options: fzf.Options(options.Picker, initialState.Prompt),
		Input: frameCandidateRecords(initial.Snapshot.Records()), Runner: dependencies.ProcessRunner,
		ForegroundTTY: terminal, TTYOut: dependencies.TTYOut, TTYErr: dependencies.TTYErr,
	})
	callback.SetCursor(protocol.CursorLine)
	closeServerErr := server.Close(context.Background())
	serverOpen = false
	if launchErr != nil {
		err = fmt.Errorf("run picker: %w", launchErr)
	} else if closeServerErr != nil {
		err = closeServerErr
	}

	var outcome protocol.Outcome
	if err == nil {
		if result.Aborted {
			outcome = protocol.Outcome{Status: protocol.StatusAborted}
		} else {
			current, currentErr := actor.Current(context.Background())
			if currentErr != nil {
				err = currentErr
			} else if options.Picker == protocol.PickerCD {
				outcome, err = session.ValidateCD(current, result.Records)
			} else {
				outcome, err = session.ValidateCP(current, result.Records, options.CWD)
			}
		}
	}
	actorErr := actor.Close()
	actorOpen = false
	if err == nil && actorErr != nil {
		err = actorErr
	}
	if err != nil {
		return protocol.Outcome{}, err
	}
	return outcome, nil
}

func validatePickerOptions(ctx context.Context, options PickerOptions) error {
	if ctx == nil {
		return errors.New("run picker: nil context")
	}
	if options.Picker != protocol.PickerCD && options.Picker != protocol.PickerCP {
		return fmt.Errorf("run picker: invalid picker %q", options.Picker)
	}
	if options.Output != protocol.OutputNUL && options.Output != protocol.OutputNUON {
		return fmt.Errorf("run picker: invalid output %q", options.Output)
	}
	if options.ZoxidePolicy != candidate.ZoxideCached && options.ZoxidePolicy != candidate.ZoxideFresh {
		return errors.New("run picker: invalid zoxide policy")
	}
	if options.ZoxideTimeout < 0 {
		return errors.New("run picker: negative zoxide timeout")
	}
	if !filepath.IsAbs(string(options.ExecutablePath)) {
		return errors.New("run picker: executable path must be absolute")
	}
	for name, path := range map[string][]byte{"cwd": options.CWD, "home": options.Home} {
		if !filepath.IsAbs(string(path)) {
			return fmt.Errorf("run picker: %s must be absolute", name)
		}
		info, err := os.Stat(string(path))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("run picker: %s must be an existing directory", name)
		}
	}
	return context.Cause(ctx)
}

func sessionBuilder(options PickerOptions, dependencies Dependencies) (*candidate.Builder, error) {
	builder := dependencies.CandidateBuilder
	path := dependencies.ZoxidePath
	if path == "" {
		path = "zoxide"
	}
	environment := process.SanitizeEnv(dependencies.Environment, nil)
	newCache := func() (*candidate.ZoxideCache, error) {
		return candidate.NewZoxideCache(dependencies.ProcessRunner, path, environment, options.ZoxideTimeout)
	}
	if options.ZoxidePolicy == candidate.ZoxideCached {
		cache, err := newCache()
		if err != nil {
			return nil, err
		}
		builder.ConfigureCached(cache)
	} else {
		builder.ConfigureFresh(newCache)
	}
	return &builder, nil
}

func frameCandidateRecords(records []candidate.Record) []byte {
	wire := make([]protocol.WireRecord, len(records))
	for index, record := range records {
		wire[index] = record.Wire()
	}
	return protocol.FrameRecords(wire)
}

type pickerBackend struct {
	actor   *session.Actor
	metrics *pickerMetrics
}

func (backend *pickerBackend) HandleEvent(ctx context.Context, event protocol.Event) (protocol.Effect, error) {
	if cause := context.Cause(ctx); cause != nil {
		return protocol.Effect{}, cause
	}
	started := time.Now()
	result, err := session.Handle(ctx, backend.actor, event)
	if err == nil {
		backend.metrics.recordTransition(result)
	}
	backend.metrics.recordCallback(time.Since(started))
	return result.Effect, err
}

func (backend *pickerBackend) LoadGeneration(ctx context.Context, generation uint64) ([]byte, error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	started := time.Now()
	snapshot, err := backend.actor.Snapshot(ctx, generation)
	if err != nil {
		return nil, err
	}
	backend.metrics.recordLoad(time.Since(started))
	return frameCandidateRecords(snapshot.Records()), nil
}

func (backend *pickerBackend) ResolvePreview(ctx context.Context, current []byte) (protocol.ResolvedCandidate, error) {
	if cause := context.Cause(ctx); cause != nil {
		return protocol.ResolvedCandidate{}, cause
	}
	record, err := backend.actor.ResolveCurrent(ctx, current)
	if err != nil {
		return protocol.ResolvedCandidate{}, err
	}
	if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem {
		return protocol.ResolvedCandidate{}, session.ErrUnknownRecord
	}
	started := time.Now()
	info, err := os.Stat(string(record.Path))
	if err != nil {
		return protocol.ResolvedCandidate{}, err
	}
	if cause := context.Cause(ctx); cause != nil {
		return protocol.ResolvedCandidate{}, cause
	}
	backend.metrics.recordPreview(time.Since(started))
	return protocol.ResolvedCandidate{Kind: record.Kind, Path: append([]byte(nil), record.Path...), Size: info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}, nil
}

func (backend *pickerBackend) RecordPreview(ctx context.Context, _ sessionipc.PreviewRequest) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	backend.metrics.recordPreview(0)
	return nil
}

type pickerMetrics struct {
	mu                       sync.Mutex
	traceID                  [16]byte
	events, loads, previews  uint64
	callbackIPC, loadLatency time.Duration
	queueWait, transform     time.Duration
	sources                  candidate.SourceMetrics
}

func (metrics *pickerMetrics) recordTransition(result session.TransitionResult) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.events = boundedIncrement(metrics.events)
	metrics.queueWait += result.Metrics.QueueWait
	metrics.transform += result.Metrics.TransformDuration
	metrics.sources.LocalDuration += result.Metrics.Sources.LocalDuration
	metrics.sources.ZoxideDuration += result.Metrics.Sources.ZoxideDuration
	metrics.sources.ZoxideOutcome = result.Metrics.Sources.ZoxideOutcome
	metrics.sources.ZoxideAttempts += result.Metrics.Sources.ZoxideAttempts
	metrics.sources.ZoxideStarts += result.Metrics.Sources.ZoxideStarts
	if result.Metrics.Sources.ZoxideMaxLive > metrics.sources.ZoxideMaxLive {
		metrics.sources.ZoxideMaxLive = result.Metrics.Sources.ZoxideMaxLive
	}
}

func (metrics *pickerMetrics) recordCallback(duration time.Duration) {
	metrics.mu.Lock()
	metrics.callbackIPC += duration
	metrics.mu.Unlock()
}

func (metrics *pickerMetrics) recordLoad(duration time.Duration) {
	metrics.mu.Lock()
	metrics.loads = boundedIncrement(metrics.loads)
	metrics.loadLatency += duration
	metrics.mu.Unlock()
}

func (metrics *pickerMetrics) recordPreview(duration time.Duration) {
	metrics.mu.Lock()
	metrics.previews = boundedIncrement(metrics.previews)
	metrics.callbackIPC += duration
	metrics.mu.Unlock()
}

func boundedIncrement(value uint64) uint64 {
	if value < 1_000_000 {
		return value + 1
	}
	return value
}
