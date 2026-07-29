package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/callback"
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/preview"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type Streams struct {
	Out io.Writer
	Err io.Writer
}

func Main(ctx context.Context, args []string, streams Streams, build string) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(streams.Out, "shell-picker %s\n", Version(build))
		return 0
	}
	if len(args) >= 1 && args[0] == "--fzf-shell" {
		return callbackMain(ctx, args, streams)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(streams.Err, "shell-picker: executable unavailable")
		return 1
	}
	dependencies := &Dependencies{
		ProcessRunner: process.Runner{}, Environment: os.Environ(), TTYErr: streams.Err,
	}
	return runPickerCLI(ctx, args, streams, executable, dependencies)
}

func runPickerCLI(ctx context.Context, args []string, streams Streams, executable string, dependencies *Dependencies) int {
	options, err := parsePickerArgs(args, executable)
	if err != nil {
		fmt.Fprintln(streams.Err, "usage: shell-picker cd|cp --cwd PATH --home PATH [--output nul|nuon] [--fzf PATH] [--zoxide-policy cached|fresh] [--zoxide-timeout DURATION]")
		return 2
	}
	outcome, err := RunPicker(ctx, options, *dependencies)
	if err != nil {
		fmt.Fprintln(streams.Err, "shell-picker: picker failed")
		return 1
	}
	if err := protocol.EncodeOutcome(streams.Out, options.Output, outcome); err != nil {
		fmt.Fprintln(streams.Err, "shell-picker: output failed")
		return 1
	}
	return 0
}

func parsePickerArgs(args []string, executable string) (PickerOptions, error) {
	if len(args) == 0 || (args[0] != string(protocol.PickerCD) && args[0] != string(protocol.PickerCP)) {
		return PickerOptions{}, errors.New("invalid picker command")
	}
	options := PickerOptions{Picker: protocol.Picker(args[0]), Output: protocol.OutputNUL, FZFPath: "fzf",
		ExecutablePath: executable, ZoxidePolicy: candidate.ZoxideCached, ZoxideTimeout: candidate.DefaultZoxideTimeout()}
	seen := make(map[string]bool)
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || !strings.HasPrefix(args[index], "--") || seen[args[index]] {
			return PickerOptions{}, errors.New("invalid or duplicate picker flag")
		}
		flag, value := args[index], args[index+1]
		seen[flag] = true
		switch flag {
		case "--cwd":
			options.CWD = []byte(value)
		case "--home":
			options.Home = []byte(value)
		case "--output":
			options.Output = protocol.OutputFormat(value)
			if options.Output != protocol.OutputNUL && options.Output != protocol.OutputNUON {
				return PickerOptions{}, errors.New("invalid output format")
			}
		case "--fzf":
			if value == "" {
				return PickerOptions{}, errors.New("empty fzf path")
			}
			options.FZFPath = value
		case "--zoxide-policy":
			policy, err := candidate.ParseZoxidePolicy(value)
			if err != nil {
				return PickerOptions{}, err
			}
			options.ZoxidePolicy = policy
		case "--zoxide-timeout":
			timeout, err := time.ParseDuration(value)
			if err != nil || timeout < 0 {
				return PickerOptions{}, errors.New("invalid zoxide timeout")
			}
			options.ZoxideTimeout = timeout
		default:
			return PickerOptions{}, errors.New("unknown picker flag")
		}
	}
	if !seen["--cwd"] || !seen["--home"] || !filepath.IsAbs(string(options.CWD)) || !filepath.IsAbs(string(options.Home)) {
		return PickerOptions{}, errors.New("cwd and home must be absolute")
	}
	if !filepath.IsAbs(executable) {
		return PickerOptions{}, errors.New("executable path must be absolute")
	}
	return options, nil
}

func callbackMain(ctx context.Context, args []string, streams Streams) int {
	if len(args) != 2 {
		fmt.Fprintln(streams.Err, "invalid callback command")
		return 2
	}
	command, err := callback.Parse(args[1])
	if err != nil {
		fmt.Fprintln(streams.Err, "invalid callback command")
		return 2
	}
	if err := callback.ValidateLocal(command, os.Getenv); err != nil {
		fmt.Fprintln(streams.Err, "invalid callback command")
		return 2
	}
	client, err := sessionipc.NewClientFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(streams.Err, "callback connection unavailable")
		return 1
	}
	defer client.CloseIdleConnections()
	columns, lines := previewDimensions(os.Getenv)
	environment := previewEnvironment(os.Environ())
	dependencies := callback.Dependencies{Client: client, LookupEnv: os.Getenv, Stdout: streams.Out, Stderr: streams.Err}
	dependencies.Preview = func(renderCtx context.Context, candidate protocol.ResolvedCandidate, stdout, stderr io.Writer) error {
		runner := process.Runner{Observe: func(event process.ProcessEvent) {
			callback.ObservePreviewProcess(renderCtx, event)
		}}
		return preview.Render(renderCtx, candidate, preview.Options{Columns: columns, Lines: lines,
			Environment: environment, Runner: runner, Limits: preview.DefaultLimits,
			Stdout: stdout, Stderr: stderr, OnDispatch: func(renderer string, pid int, duration time.Duration) {
				callback.ObservePreviewDispatch(renderCtx, renderer, pid, duration)
			}})
	}
	if err := callback.Dispatch(ctx, command, dependencies); err != nil {
		if errors.Is(err, callback.ErrGrammar) || errors.Is(err, callback.ErrKey) {
			fmt.Fprintln(streams.Err, "invalid callback command")
			return 2
		}
		fmt.Fprintln(streams.Err, "callback failed")
		return 1
	}
	return 0
}

func previewDimensions(lookup func(string) string) (int, int) {
	return previewDimension(lookup("FZF_PREVIEW_COLUMNS"), 80), previewDimension(lookup("FZF_PREVIEW_LINES"), 40)
}

func previewDimension(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	for _, value := range []byte(raw) {
		if value < '0' || value > '9' {
			return fallback
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return min(value, 1000)
}

func previewEnvironment(inherited []string) []string {
	allowed := map[string]bool{
		"PATH": true, "LANG": true, "LANGUAGE": true, "LC_ALL": true, "LC_CTYPE": true,
		"LC_MESSAGES": true, "LC_COLLATE": true, "LC_MONETARY": true, "LC_NUMERIC": true,
		"LC_TIME": true, "LC_PAPER": true, "LC_NAME": true, "LC_ADDRESS": true,
		"LC_TELEPHONE": true, "LC_MEASUREMENT": true, "LC_IDENTIFICATION": true,
		"TERM": true, "COLORTERM": true, "NO_COLOR": true,
	}
	environment := make([]string, 0, len(allowed))
	for _, entry := range inherited {
		key, _, ok := strings.Cut(entry, "=")
		lookup := key
		if runtime.GOOS == "windows" {
			lookup = strings.ToUpper(key)
		}
		if ok && allowed[lookup] {
			environment = append(environment, entry)
		}
	}
	return process.SanitizeEnv(environment, nil)
}
