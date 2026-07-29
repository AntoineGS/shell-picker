package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	builder, err := sessionBuilder(options, &dependencies)
	if err != nil {
		return protocol.Outcome{}, err
	}
	actorCtx, cancelActor := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelActor(nil)
	actor := session.New(actorCtx, builder.Build)
	actorOpen := true
	defer func() {
		if actorOpen {
			if cause := context.Cause(ctx); cause != nil {
				cancelActor(cause)
			}
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
	server, err := sessionipc.Listen(ctx, token, backend)
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
	} else if cause := context.Cause(ctx); cause != nil {
		err = cause
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
	if cause := context.Cause(ctx); cause != nil {
		cancelActor(cause)
		if err == nil {
			err = cause
		}
	}
	actorErr := actor.Close()
	actorOpen = false
	if err == nil && actorErr != nil {
		err = actorErr
	}
	if err == nil {
		err = context.Cause(ctx)
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

func sessionBuilder(options PickerOptions, dependencies *Dependencies) (*candidate.Builder, error) {
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
		dependencies.CandidateBuilder.ConfigureCached(cache)
	} else {
		dependencies.CandidateBuilder.ConfigureFresh(newCache)
	}
	return &dependencies.CandidateBuilder, nil
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
	backend.metrics.recordPreviewResolve(time.Since(started))
	return protocol.ResolvedCandidate{Kind: record.Kind, Path: append([]byte(nil), record.Path...), Size: info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}, nil
}

func (backend *pickerBackend) RecordPreview(ctx context.Context, request sessionipc.PreviewRequest) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	started := time.Now()
	if err := backend.metrics.recordPreview(request); err != nil {
		return err
	}
	backend.metrics.recordCallback(time.Since(started))
	return context.Cause(ctx)
}
