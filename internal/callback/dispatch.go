package callback

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/preview"
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

var ErrKey = errors.New("callback: unexpected fzf key")

type IPCClient interface {
	Event(context.Context, sessionipc.EventRequest) (sessionipc.EventResponse, error)
	FinalizeEvent(context.Context, sessionipc.EventFinalizeRequest) error
	FinalizeLoad(context.Context, sessionipc.LoadFinalizeRequest) error
	Load(context.Context, sessionipc.LoadRequest) ([]byte, error)
	ResolvePreview(context.Context, sessionipc.PreviewRequest) (sessionipc.PreviewResponse, error)
	RecordPreview(context.Context, sessionipc.PreviewRequest) error
	Display(context.Context) (sessionipc.DisplayResponse, error)
}

type Dependencies struct {
	Client    IPCClient
	LookupEnv func(string) string
	Stdout    io.Writer
	Stderr    io.Writer
	Preview   func(context.Context, protocol.ResolvedCandidate, io.Writer, io.Writer) error
}

func Dispatch(ctx context.Context, command Command, dependencies Dependencies) error {
	if ctx == nil || dependencies.LookupEnv == nil || dependencies.Stdout == nil {
		return errors.New("callback: incomplete dependencies")
	}
	if !command.Local() && dependencies.Client == nil {
		return errors.New("callback: incomplete dependencies")
	}
	if err := ValidateLocal(command, dependencies.LookupEnv); err != nil {
		return err
	}
	switch command.Kind {
	case KindEvent:
		return dispatchEvent(ctx, command, dependencies)
	case KindLoad:
		return dispatchLoad(ctx, command, dependencies)
	case KindPreview:
		return dispatchPreview(ctx, dependencies)
	case KindDisplay:
		return dispatchDisplay(ctx, dependencies)
	case KindInfo:
		return writeAll(dependencies.Stdout, []byte(finderInfo(command.Picker, dependencies.LookupEnv)))
	case KindEmptySource:
		return nil
	case KindInvalidPreview:
		return writeAll(dependencies.Stdout, []byte("[Invalid Path]"))
	default:
		return ErrGrammar
	}
}

func ValidateLocal(command Command, lookupEnv func(string) string) error {
	if lookupEnv == nil {
		return errors.New("callback: nil environment reader")
	}
	switch command.Kind {
	case KindEvent:
		if command.Opcode != protocol.OpRestoreView && !validKey(command.Opcode, lookupEnv("FZF_KEY")) {
			return ErrKey
		}
	case KindLoad, KindPreview, KindDisplay, KindInfo, KindEmptySource, KindInvalidPreview:
		return nil
	default:
		return ErrGrammar
	}
	return nil
}

func dispatchEvent(ctx context.Context, command Command, dependencies Dependencies) (err error) {
	key := dependencies.LookupEnv("FZF_KEY")
	response, err := dependencies.Client.Event(ctx, sessionipc.EventRequest{
		Opcode: command.Opcode, Key: key,
		QueryBase64:       base64.StdEncoding.EncodeToString([]byte(dependencies.LookupEnv("FZF_QUERY"))),
		CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte(dependencies.LookupEnv("FZF_CURRENT_ITEM"))),
	})
	if err != nil {
		return err
	}
	if response.EventID == 0 {
		return dispatchEventAction(response, dependencies)
	}
	applied := false
	defer func() {
		if finalizeErr := dependencies.Client.FinalizeEvent(context.WithoutCancel(ctx), sessionipc.EventFinalizeRequest{
			EventID: response.EventID, Applied: applied,
		}); err == nil && finalizeErr != nil {
			err = finalizeErr
		}
	}()
	if err := dispatchEventAction(response, dependencies); err != nil {
		return err
	}
	applied = true
	return nil
}

func dispatchEventAction(response sessionipc.EventResponse, dependencies Dependencies) error {
	if response.Effect.Cursor != "" {
		SetCursor(response.Effect.Cursor)
	}
	if response.Effect.Header != "" {
		header, ok := visibleHeader(response.Effect.Header, dependencies.LookupEnv)
		if ok {
			response.Effect.Header = header
		} else {
			response.Effect.Header = ""
		}
	}
	action, err := fzf.RenderEffectForEvent(response.Effect, response.EventID)
	if err != nil {
		return err
	}
	return writeAll(dependencies.Stdout, []byte(action))
}

func dispatchDisplay(ctx context.Context, dependencies Dependencies) error {
	response, err := dependencies.Client.Display(ctx)
	if err != nil {
		return err
	}
	header, ok := visibleHeader(response.Header, dependencies.LookupEnv)
	if !ok {
		return nil
	}
	action, err := fzf.RenderEffect(protocol.Effect{Header: header})
	if err != nil {
		return err
	}
	return writeAll(dependencies.Stdout, []byte(action))
}

func dispatchLoad(ctx context.Context, command Command, dependencies Dependencies) (err error) {
	applied := false
	if command.EventID != 0 {
		defer func() {
			if finalizeErr := dependencies.Client.FinalizeLoad(context.WithoutCancel(ctx), sessionipc.LoadFinalizeRequest{EventID: command.EventID, Applied: applied}); err == nil && finalizeErr != nil {
				err = finalizeErr
			}
		}()
	}
	data, err := dependencies.Client.Load(ctx, sessionipc.LoadRequest{Generation: command.Generation, EventID: command.EventID})
	if err != nil {
		return err
	}
	if err := writeAll(dependencies.Stdout, data); err != nil {
		return err
	}
	applied = true
	return nil
}

func writeAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if written < 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func dispatchPreview(ctx context.Context, dependencies Dependencies) error {
	if dependencies.Preview == nil {
		return errors.New("callback: preview renderer unavailable")
	}
	rawCurrent := dependencies.LookupEnv("FZF_CURRENT_ITEM")
	if rawCurrent == "" {
		// fzf can request a preview while a reload is replacing the candidate
		// list. There is no candidate to resolve during that transient state.
		return nil
	}
	current := base64.StdEncoding.EncodeToString([]byte(rawCurrent))
	request := sessionipc.PreviewRequest{Phase: "resolve", CurrentItemBase64: current}
	response, err := dependencies.Client.ResolvePreview(ctx, request)
	if err != nil {
		if errors.Is(err, sessionipc.ErrNotFound) {
			// fzf can ask for a preview of the item being replaced by a reload.
			// The item is no longer resolvable, so leave the preview unchanged
			// instead of reporting a transient callback failure to fzf.
			return nil
		}
		return err
	}
	path, err := decodeCanonical(response.PathBase64)
	if err != nil {
		return err
	}
	if response.Kind == protocol.KindVirtual || !filepath.IsAbs(string(path)) {
		return errors.New("callback: resolved preview is not an absolute filesystem candidate")
	}
	candidate := protocol.ResolvedCandidate{Kind: response.Kind, Path: path, Size: response.Size,
		ModTimeUnixNano: response.ModTimeUnixNano, Mode: response.Mode}
	started := time.Now()
	state := &previewTelemetry{parent: ctx, client: dependencies.Client, current: current, renderer: "native"}
	renderCtx := context.WithValue(ctx, previewTelemetryKey{}, state)
	renderErr := dependencies.Preview(renderCtx, candidate, dependencies.Stdout, dependencies.Stderr)
	state.ensureStarted()
	if errors.Is(renderErr, preview.ErrTerminalResource) {
		return renderErr
	}
	duration := time.Since(started)
	if duration > 10*time.Second {
		duration = 10 * time.Second
	}
	outcome := "ok"
	if renderErr != nil {
		outcome = "error"
	}
	renderer, childStarts, maxLive, invalid := state.snapshot()
	recordSoft(ctx, dependencies.Client, sessionipc.PreviewRequest{Phase: "finished", CurrentItemBase64: current,
		Renderer: renderer, DurationUS: duration.Microseconds(), ChildStarts: childStarts,
		MaxLiveChildren: maxLive, Outcome: outcome})
	if invalid && renderErr == nil {
		return errors.New("callback: preview child limit exceeded")
	}
	return renderErr
}

type previewTelemetryKey struct{}

type previewTelemetry struct {
	mu          sync.Mutex
	parent      context.Context
	client      IPCClient
	current     string
	renderer    string
	started     bool
	childStarts int
	live        int
	maxLive     int
	invalid     bool
}

func ObservePreviewDispatch(ctx context.Context, renderer string, _ int, duration time.Duration) {
	state, _ := ctx.Value(previewTelemetryKey{}).(*previewTelemetry)
	if state == nil || duration != 0 {
		return
	}
	state.mu.Lock()
	if renderer != "" {
		if renderer == "native" && state.childStarts > 0 && state.renderer != "native" {
			state.renderer += "-fallback"
		} else {
			state.renderer = renderer
		}
	}
	shouldRecord := !state.started
	state.started = true
	request := sessionipc.PreviewRequest{Phase: "started", CurrentItemBase64: state.current, Renderer: state.renderer}
	state.mu.Unlock()
	if shouldRecord {
		recordSoft(state.parent, state.client, request)
	}
}

func ObservePreviewProcess(ctx context.Context, event processpkg.ProcessEvent) {
	state, _ := ctx.Value(previewTelemetryKey{}).(*previewTelemetry)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	switch event.Phase {
	case "start":
		state.childStarts++
		state.live++
		if state.live > state.maxLive {
			state.maxLive = state.live
		}
	case "exit":
		if state.live > 0 {
			state.live--
		}
	}
	state.invalid = state.childStarts > 3 || state.maxLive > 1
}

func (state *previewTelemetry) ensureStarted() {
	state.mu.Lock()
	shouldRecord := !state.started
	state.started = true
	request := sessionipc.PreviewRequest{Phase: "started", CurrentItemBase64: state.current, Renderer: state.renderer}
	state.mu.Unlock()
	if shouldRecord {
		recordSoft(state.parent, state.client, request)
	}
}

func (state *previewTelemetry) snapshot() (string, int, int, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	invalid := state.invalid || state.live != 0 || state.renderer == "native" && (state.childStarts != 0 || state.maxLive != 0)
	return state.renderer, state.childStarts, state.maxLive, invalid
}

func recordSoft(parent context.Context, client IPCClient, request sessionipc.PreviewRequest) {
	soft, cancel := context.WithTimeout(context.WithoutCancel(parent), 250*time.Millisecond)
	defer cancel()
	_ = client.RecordPreview(soft, request)
}

func decodeCanonical(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("callback: invalid resolved path encoding")
	}
	return decoded, nil
}

func validKey(opcode protocol.Opcode, key string) bool {
	allowed := map[protocol.Opcode]map[string]bool{
		protocol.OpModeInsert: {"i": true}, protocol.OpModeAdd: {"a": true}, protocol.OpEscape: {"esc": true},
		protocol.OpForward: {"ctrl-l": true, "tab": true, "right": true, "l": true},
		protocol.OpParent:  {"ctrl-h": true, "left": true, "h": true},
		protocol.OpSlash:   {"/": true}, protocol.OpHome: {"~": true}, protocol.OpEnter: {"enter": true},
	}
	keys, ok := allowed[opcode]
	return ok && keys[key]
}
