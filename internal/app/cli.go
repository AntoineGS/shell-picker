package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/callback"
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
	fmt.Fprintln(streams.Err, "usage: shell-picker version")
	return 2
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
	dependencies := callback.Dependencies{Client: client, LookupEnv: os.Getenv, Stdout: streams.Out, Stderr: streams.Err}
	dependencies.Preview = func(renderCtx context.Context, candidate protocol.ResolvedCandidate, stdout, stderr io.Writer) error {
		runner := process.Runner{Observe: func(event process.ProcessEvent) {
			callback.ObservePreviewProcess(renderCtx, event)
		}}
		return preview.Render(renderCtx, candidate, preview.Options{Columns: 80, Lines: 40,
			Environment: previewEnvironment(os.Environ()), Runner: runner, Limits: preview.DefaultLimits,
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
