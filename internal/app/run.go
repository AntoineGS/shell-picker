package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/AntoineGS/shell-picker/internal/callback"
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type PickerOptions struct {
	Completion     bool
	Query          string
	CommandLine    string
	Display        string
	FilesOnly      bool
	Picker         protocol.Picker
	CWD            []byte
	Home           []byte
	Output         protocol.OutputFormat
	FZFPath        string
	ExecutablePath string
	ZoxidePolicy   candidate.ZoxidePolicy
	ZoxideTimeout  time.Duration
	TracePath      string
}

type Dependencies struct {
	CandidateBuilder candidate.Builder
	ProcessRunner    process.Runner
	ZoxidePath       string
	Environment      []string
	ForegroundTTY    *os.File
	TTYOut           io.Writer
	TTYErr           io.Writer

	launchFZF                 func(context.Context, fzf.Config) (fzf.Result, error)
	newFZFSidecar             fzfSidecarFactory
	listenIPC                 func(context.Context, sessionipc.Token, sessionipc.Backend) (*sessionipc.Server, error)
	buildLocal                func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error)
	loadInitialZoxide         func(context.Context) (candidate.InitialZoxideResult, error)
	IndexLoader               IndexLoader
	beforeInitialInputPublish func(*fzf.InputStream)
}

func RunPicker(ctx context.Context, options PickerOptions, dependencies Dependencies) (outcome protocol.Outcome, err error) {
	if err := validatePickerOptions(ctx, options); err != nil {
		return protocol.Outcome{}, err
	}
	sidecarEnabled := fzfsidecar.Enabled(dependencies.Environment)
	var traceID [16]byte
	if _, err := rand.Read(traceID[:]); err != nil {
		return protocol.Outcome{}, errors.New("generate trace session ID")
	}
	trace, err := openPickerTrace(options.TracePath, traceID, dependencies.TTYErr)
	if err != nil {
		return protocol.Outcome{}, err
	}
	if trace != nil {
		trace.event(integrationpkg.TraceEvent{Name: "session.start", Outcome: string(options.Picker)})
		defer func() {
			outcome, err = trace.finish(outcome, err)
		}()
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
	buildLocal := dependencies.buildLocal
	if buildLocal == nil {
		buildLocal = builder.BuildLocal
	}
	indexed := newIndexedSource(string(options.Home), dependencies.Environment, dependencies.IndexLoader)
	generate := sessionGenerator(options, buildLocal, indexed, trace)
	actorCtx, cancelActor := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelActor(nil)
	actor := session.New(actorCtx, generate)
	input := fzf.NewInputStream(nil)
	var fzfInput io.ReadCloser = input
	streamInitial := options.Picker == protocol.PickerSearch && supportsInitialIndexedStream() &&
		dependencies.launchFZF == nil
	var pipeReader *io.PipeReader
	var pipeWriter *io.PipeWriter
	var initialGate *initialStreamGate
	var streamRun *initialStreamRun
	var cancelFZF context.CancelCauseFunc
	if streamInitial {
		pipeReader, pipeWriter = io.Pipe()
		fzfInput = pipeReader
		initialGate = &initialStreamGate{}
	}
	metrics := &pickerMetrics{traceID: traceID, policy: options.ZoxidePolicy,
		sources: candidate.SourceMetrics{ZoxideOutcome: "not-run"}}
	var coordinator *initialEnrichment
	var server *sessionipc.Server
	var sidecar fzfSidecarLifecycle
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true

		stopCause := context.Cause(ctx)
		if stopCause == nil && err != nil {
			stopCause = err
		}

		var sidecarErr, coordinatorErr, serverErr, actorErr error
		sidecarErr = sidecar.stopAndWait()
		if coordinator != nil {
			coordinatorErr = coordinator.Stop(stopCause)
			coordinatorErr = joinLifecycleErrors(coordinatorErr, coordinator.Wait())
		} else if input != nil {
			_ = input.Close()
		}
		if streamRun != nil {
			streamRun.stop(stopCause)
			_ = streamRun.wait()
		}
		if pipeReader != nil {
			_ = pipeReader.Close()
		}
		if cancelFZF != nil {
			cancelFZF(stopCause)
		}
		if server != nil {
			serverErr = server.Close(context.Background())
		}
		cancelActor(stopCause)
		actorErr = actor.Close()

		if err == nil {
			err = sidecarErr
			if err == nil {
				err = coordinatorErr
			}
			if err == nil {
				err = serverErr
			}
		}
		err = selectLifecycleError(err, actorErr, context.Cause(ctx))
	}
	defer cleanup()

	initialLocation := pathutil.Filesystem(options.CWD)
	if options.Completion {
		resolved := pathutil.ResolveCompletion(string(options.CWD), string(options.Home), options.Query)
		initialLocation = pathutil.Filesystem([]byte(resolved.Directory))
	}
	initialPrompt := "[I] "
	initialSource := protocol.SourceBrowser
	if options.Picker == protocol.PickerSearch {
		initialSource = protocol.SourceIndexed
	}
	initialState := session.State{
		Picker: options.Picker, Source: initialSource, Mode: protocol.ModeInsert,
		Location: initialLocation, Home: pathutil.Filesystem(options.Home), Prompt: initialPrompt,
		Browser: &session.BrowserContext{
			Location: initialLocation, Mode: protocol.ModeInsert, SelectedPaths: [][]byte{},
		},
	}
	if options.Completion {
		initialState.Completion = &session.CompletionContext{Base: string(options.CWD)}
		initialState.Query = []byte(options.Query)
		initialState.Browser.Query = []byte(options.Query)
	}
	if options.Picker == protocol.PickerSearch {
		initialState.Indexed = &session.IndexedContext{SelectedPaths: [][]byte{}, FilesOnly: options.FilesOnly}
		if streamInitial {
			initialState.Query = []byte(options.Query)
			initialState.Indexed.Query = []byte(options.Query)
		}
	}
	initialHeader := pathutil.PromptDisplayHome(initialLocation, initialState.Home)
	if initialSource == protocol.SourceIndexed {
		initialHeader = indexed.header(options.Picker, initialState.FilesSearch())
	}
	if options.Picker == protocol.PickerCD && (!options.Completion || string(initialLocation.Path) == string(options.CWD)) {
		var source initialZoxideLoader = builder
		if dependencies.loadInitialZoxide != nil {
			source = initialZoxideLoaderFunc(dependencies.loadInitialZoxide)
		}
		coordinator, err = newInitialEnrichment(ctx, actor, source, input, metrics, trace, options.ZoxidePolicy, options.Home)
		if err != nil {
			return protocol.Outcome{}, err
		}
	}
	initialBuild := candidate.BuildRequest{Picker: options.Picker, Source: initialSource, Location: initialState.Location,
		Initial: initialSource == protocol.SourceBrowser, FilesOnly: options.FilesOnly}
	initialEffect := protocol.Effect{Mode: protocol.ModeInsert, Prompt: initialState.Prompt, Search: "on",
		Rebind: protocol.ModeInsert, Cursor: protocol.CursorLine}
	initialProposal := session.ProposedTransition{State: initialState, Build: &initialBuild, Effect: initialEffect}
	if streamInitial {
		initialProposal.Build = nil
	}
	initial, err := actor.Apply(ctx, initialProposal)
	if err != nil {
		return protocol.Outcome{}, fmt.Errorf("build initial candidates: %w", err)
	}
	if !streamInitial {
		traceInitialTransition(trace, options.ZoxidePolicy, options.Picker, initial, initialState.Location.Path)
		metrics.recordTransition(initial)
	}
	if initialSource == protocol.SourceIndexed {
		initialHeader = indexed.header(options.Picker, initialState.FilesSearch())
	}
	if !streamInitial && dependencies.beforeInitialInputPublish != nil {
		dependencies.beforeInitialInputPublish(input)
	}
	if !streamInitial {
		if err := input.Append(initial.Snapshot.FramedRecords()); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return protocol.Outcome{}, cause
			}
			return protocol.Outcome{}, fmt.Errorf("publish initial candidates: %w", err)
		}
	}
	if coordinator != nil {
		if err := coordinator.Activate(initial.Snapshot.Generation()); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return protocol.Outcome{}, cause
			}
			return protocol.Outcome{}, fmt.Errorf("activate initial candidates: %w", err)
		}
	} else if !streamInitial {
		if err := input.Close(); err != nil {
			return protocol.Outcome{}, fmt.Errorf("close picker input: %w", err)
		}
	}

	token, err := sessionipc.NewToken()
	if err != nil {
		return protocol.Outcome{}, err
	}
	commandHeader := commandLineHeader(options.CommandLine)
	initialHeader = pickerHeader(commandHeader, initialState, indexed)
	if streamInitial {
		initialHeader = loadingIndexHeader(commandHeader, options.FilesOnly)
	}
	backend := &pickerBackend{actor: actor, metrics: metrics, trace: trace, enrichment: coordinator,
		indexed: indexed, commandHeader: commandHeader, initialGate: initialGate}
	var listenedServer *sessionipc.Server
	listenIPC := dependencies.listenIPC
	if listenIPC == nil {
		listenIPC = sessionipc.Listen
	}
	listenedServer, err = listenIPC(ctx, token, backend)
	if err != nil {
		return protocol.Outcome{}, err
	}
	server = listenedServer

	if err := sidecar.create(sidecarEnabled, options.Picker, dependencies.newFZFSidecar, newSidecarTraceObserver(trace)); err != nil {
		return protocol.Outcome{}, err
	}

	callback.SetCursor(protocol.CursorLine)
	launch := dependencies.launchFZF
	if launch == nil {
		launch = fzf.Run
	}
	fzfRunner := dependencies.ProcessRunner
	processObserver := fzfRunner.Observe
	fzfRunner.Observe = func(event process.ProcessEvent) {
		if processObserver != nil {
			processObserver(event)
		}
		if event.Phase == "start" {
			trace.event(integrationpkg.TraceEvent{Name: "fzf.start", Outcome: "ok"})
		}
	}
	listenAddress, listenAPIKey := sidecar.credentials()
	fzfOptions, err := fzf.Options(fzf.OptionsConfig{
		Picker: options.Picker, Prompt: initialPrompt, Header: initialHeader, ListenAddress: listenAddress,
		Completion: options.Completion, Query: options.Query, Height: pickerHeight(options.Display, terminal),
		InitialLoading: streamInitial,
	})
	if err != nil {
		return protocol.Outcome{}, fmt.Errorf("build fzf options: %w", err)
	}
	sidecar.start(ctx)
	launchCtx := ctx
	if streamInitial {
		var streamCtx context.Context
		streamCtx, cancelFZF = context.WithCancelCause(ctx)
		launchCtx = streamCtx
		streamRun = startInitialStream(ctx, pipeWriter, func(streamCtx context.Context, writer io.Writer) error {
			trace.event(integrationpkg.TraceEvent{Name: "generation.start", Generation: initial.Snapshot.Generation() + 1, Outcome: "ok"})
			database, records, streamErr := indexed.streamInitial(streamCtx, initialBuild, writer)
			if streamErr != nil {
				trace.event(integrationpkg.TraceEvent{Name: "generation.discard", Generation: initial.Snapshot.Generation() + 1, Outcome: traceDiscardOutcome(streamErr)})
				// fzf owns the read side; its exit can close the pipe before
				// Run returns its (possibly ordinary) abort result.
				if !errors.Is(streamErr, io.ErrClosedPipe) {
					cancelFZF(streamErr)
				}
				return streamErr
			}
			final, applyErr := actor.Enrich(streamCtx, initial.Snapshot.Generation(), records, candidate.SourceMetrics{})
			if applyErr != nil {
				cancelFZF(applyErr)
				return fmt.Errorf("install initial indexed generation: %w", applyErr)
			}
			indexed.cache(database)
			traceInitialTransition(trace, options.ZoxidePolicy, options.Picker, final, initialState.Location.Path)
			metrics.recordTransition(final)
			initialGate.markActorReady()
			return nil
		})
	}
	result, launchErr := launch(launchCtx, fzf.Config{
		Picker: options.Picker, FZFPath: options.FZFPath, ExecutablePath: options.ExecutablePath,
		Environment: process.SanitizeEnv(dependencies.Environment, nil), TracePath: options.TracePath,
		TraceSession: integrationpkg.RedactedSessionID(traceID), CallbackAddress: server.Address(),
		CallbackToken: token.String(), ListenAPIKey: listenAPIKey, Options: fzfOptions,
		Input: fzfInput, Runner: fzfRunner,
		ForegroundTTY: terminal, TTYOut: dependencies.TTYOut, TTYErr: dependencies.TTYErr,
	})
	if streamRun != nil {
		if launchErr != nil || result.Aborted || !initialGate.ready() {
			streamRun.stop(context.Canceled)
		}
		streamErr := streamRun.wait()
		if streamErr != nil && !errors.Is(streamErr, context.Canceled) && context.Cause(ctx) == nil &&
			!(result.Aborted && errors.Is(streamErr, io.ErrClosedPipe)) {
			err = fmt.Errorf("load initial indexed candidates: %w", streamErr)
		}
		if err == nil && launchErr == nil && !result.Aborted && !initialGate.ready() && context.Cause(ctx) == nil {
			err = errors.New("fzf exited before the initial index was ready")
		}
	}
	fzfOutcome := "ok"
	if launchErr != nil {
		fzfOutcome = "error"
	} else if result.Aborted {
		fzfOutcome = "aborted"
	}
	trace.event(integrationpkg.TraceEvent{Name: "fzf.exit", Outcome: fzfOutcome})
	callback.SetCursor(protocol.CursorLine)
	if launchErr != nil && err == nil {
		err = fmt.Errorf("run picker: %w", launchErr)
	}
	parentCause := context.Cause(ctx)

	if err == nil && parentCause == nil {
		if result.Aborted {
			outcome = protocol.Outcome{Status: protocol.StatusAborted}
		} else {
			current, currentErr := actor.Current(context.Background())
			if currentErr != nil {
				err = currentErr
			} else if options.Completion {
				outcome, err = completionOutcome(current, result.Records, options)
			} else if current.State().FilesSearch() {
				outcome, err = session.ValidateSearch(current, result.Records)
				outcome.Insert = err == nil && options.Picker != protocol.PickerSearch
			} else if options.Picker == protocol.PickerCD {
				outcome, err = session.ValidateCD(current, result.Records)
			} else if options.Picker == protocol.PickerSearch {
				outcome, err = session.ValidateSearch(current, result.Records)
			} else {
				outcome, err = session.ValidateCP(current, result.Records, options.CWD)
			}
		}
	}
	if latest := context.Cause(ctx); latest != nil {
		parentCause = latest
	}
	if err == nil && parentCause != nil {
		return protocol.Outcome{}, parentCause
	}
	if err != nil {
		return protocol.Outcome{}, err
	}
	return outcome, nil
}

type initialZoxideLoaderFunc func(context.Context) (candidate.InitialZoxideResult, error)

func (load initialZoxideLoaderFunc) LoadInitialZoxide(ctx context.Context) (candidate.InitialZoxideResult, error) {
	return load(ctx)
}

func selectLifecycleError(selected, actorClose, parentCause error) error {
	if selected != nil {
		return selected
	}
	if actorClose != nil {
		return actorClose
	}
	return parentCause
}
