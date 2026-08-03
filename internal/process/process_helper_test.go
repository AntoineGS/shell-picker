package process

import (
	"context"
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
	case "mark-start":
		if err := os.WriteFile(args[1], []byte("started"), 0o600); err != nil {
			os.Exit(3)
		}
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
