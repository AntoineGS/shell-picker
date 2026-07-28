package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROCESS_HELPER") != "1" {
		return
	}
	args := helperArgs()
	if len(args) == 0 {
		os.Exit(2)
	}
	if platformHelper(args) {
		os.Exit(0)
	}
	switch args[0] {
	case "print-args":
		for _, arg := range args[1:] {
			_, _ = os.Stdout.Write(append([]byte(arg), 0))
		}
	case "exit":
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "block":
		time.Sleep(30 * time.Second)
	case "pid-block":
		_, _ = fmt.Fprintln(os.Stdout, os.Getpid())
		_ = os.Stdout.Close()
		time.Sleep(30 * time.Second)
	case "both-streams":
		for i := 0; i < 200; i++ {
			_, _ = fmt.Fprintln(os.Stdout, "out")
			_, _ = fmt.Fprintln(os.Stderr, "err")
		}
	case "spawn":
		cmd := helperExec("block")
		cmd.Stdin = os.Stdin
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintln(os.Stdout, cmd.Process.Pid)
		_ = os.Stdout.Close()
		_ = cmd.Wait()
	case "hold-stdout":
		cmd := helperExec("block")
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintln(os.Stdout, cmd.Process.Pid)
	case "hold-stdout-exit":
		cmd := helperExec("block")
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintln(os.Stdout, cmd.Process.Pid)
		os.Exit(17)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func helperArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

func helperExec(mode string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestProcessHelper$", "--", mode}, args...)...)
	cmd.Env = append(os.Environ(), "GO_WANT_PROCESS_HELPER=1")
	return cmd
}

func helperSpec(mode string, args ...string) Spec {
	return Spec{Path: os.Args[0], Args: append([]string{"-test.run=^TestProcessHelper$", "--", mode}, args...),
		Env: append(os.Environ(), "GO_WANT_PROCESS_HELPER=1"), Containment: ContainmentOwnTree}
}

func TestRunnerPassesArgumentsWithoutShell(t *testing.T) {
	spec := helperSpec("print-args", "a b", `$(touch nope)`, `x&y`)
	var out bytes.Buffer
	spec.Stdout = &out
	err := (Runner{}).Run(context.Background(), spec)
	if err != nil || out.String() != "a b\x00$(touch nope)\x00x&y\x00" {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestExitErrorAndContextPrecedence(t *testing.T) {
	err := (Runner{}).Run(context.Background(), helperSpec("exit", "23"))
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("exit error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := child.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel wait=%v", err)
	}
}

func TestCancellationCauseAndExitPrecedePumpFailures(t *testing.T) {
	pumpErr := errors.New("writer failed")
	spec := helperSpec("print-args", "output")
	spec.Stdout = errorWriter{err: pumpErr}
	if err := (Runner{}).Run(context.Background(), spec); !errors.Is(err, pumpErr) {
		t.Fatalf("pump error=%v", err)
	}
	spec = helperSpec("exit", "19")
	spec.Stdout = errorWriter{err: pumpErr}
	err := (Runner{}).Run(context.Background(), spec)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 19 {
		t.Fatalf("exit precedence=%v", err)
	}
	cause := errors.New("caller stopped")
	ctx, cancel := context.WithCancelCause(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	cancel(cause)
	if err := child.Wait(); !errors.Is(err, cause) {
		t.Fatalf("cancellation cause=%v", err)
	}
}

func TestExitErrorPrecedesWaitDelay(t *testing.T) {
	spec := helperSpec("hold-stdout-exit")
	spec.WaitDelay = 100 * time.Millisecond
	var output bytes.Buffer
	spec.Stdout = &output
	err := (Runner{}).Run(context.Background(), spec)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("error=%v", err)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(output.String()))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestObservationLifecycle(t *testing.T) {
	var events []ProcessEvent
	runner := Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}
	if err := runner.Run(context.Background(), helperSpec("exit", "0")); err != nil {
		t.Fatal(err)
	}
	if got := eventPhases(events); got != "attempt,start,exit" || events[0].PID != 0 || events[1].PID <= 0 || events[2].PID != events[1].PID {
		t.Fatalf("events=%+v phases=%q", events, got)
	}
}

func TestObserveAttemptWithoutStartWhenExecutableIsMissing(t *testing.T) {
	var events []ProcessEvent
	missing := t.TempDir() + string(os.PathSeparator) + "missing"
	err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Run(
		context.Background(), Spec{Path: missing, Containment: ContainmentOwnTree})
	if !errors.Is(err, exec.ErrNotFound) || len(events) != 1 || events[0].Phase != "attempt" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestObserveAttemptWithoutStartOnSpawnFailure(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "invalid-executable"
	if err := os.WriteFile(path, []byte("not an executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	var events []ProcessEvent
	_, err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Start(
		context.Background(), Spec{Path: path, Containment: ContainmentOwnTree})
	if err == nil || len(events) != 1 || events[0].Phase != "attempt" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestWaitIsSingleUseAndWaitDelayBoundsInheritedPipe(t *testing.T) {
	spec := helperSpec("hold-stdout")
	spec.WaitDelay = 100 * time.Millisecond
	var output bytes.Buffer
	spec.Stdout = &output
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("first wait=%v", err)
	}
	if err := child.Wait(); !errors.Is(err, ErrAlreadyWaited) {
		t.Fatalf("second wait=%v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatalf("pid output=%q: %v", output.String(), err)
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

func TestStartRejectsInvalidSpec(t *testing.T) {
	for _, spec := range []Spec{{Path: os.Args[0], Containment: 99},
		{Path: os.Args[0], Containment: ContainmentOwnTree, WaitDelay: -time.Second}} {
		if _, err := (Runner{}).Start(context.Background(), spec); err == nil {
			t.Fatalf("Start(%+v) succeeded", spec)
		}
	}
}

func TestSanitizeEnvRejectsMaliciousInheritedDefaults(t *testing.T) {
	got := SanitizeEnv([]string{"PATH=/bin", "FZF_DEFAULT_OPTS=bad", "FZF_DEFAULT_OPTS_FILE=/tmp/x",
		"FZF_DEFAULT_COMMAND=bad", "FZF_KEY=x", "FZF_QUERY=y", "FZF_CURRENT_ITEM=z", "SHELL_PICKER_TOKEN=stale"},
		map[string]string{"SHELL_PICKER_TOKEN": "fresh"})
	want := []string{"PATH=/bin", "SHELL_PICKER_TOKEN=fresh"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("env=%q want=%q", got, want)
	}
}

func eventPhases(events []ProcessEvent) string {
	phases := make([]string, len(events))
	for i := range events {
		phases[i] = events[i].Phase
	}
	return strings.Join(phases, ",")
}
