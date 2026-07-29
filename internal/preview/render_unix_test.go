//go:build !windows

package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
)

func TestExternalRendererSpecInheritsUnixCallbackGroup(t *testing.T) {
	var stdout, stderr bytes.Buffer
	spec := externalProcessSpec("/usr/bin/bat", []string{"--", "/tmp/file.txt"}, []string{"PATH=/usr/bin"}, &stdout, &stderr)
	if spec.Containment != processpkg.ContainmentInheritTree || spec.Stdout != &stdout || spec.Stderr != &stderr {
		t.Fatalf("spec=%+v", spec)
	}
}

func TestExternalRendererOutputLimitKillsInheritedGroupWithoutFallback(t *testing.T) {
	testExternalRendererResourceKill(t, "output")
}

func TestExternalRendererDeadlineKillsInheritedGroupWithoutFallback(t *testing.T) {
	testExternalRendererResourceKill(t, "deadline")
}

func testExternalRendererResourceKill(t *testing.T, mode string) {
	t.Helper()
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	rendererPID := filepath.Join(root, "renderer.pid")
	descendantPID := filepath.Join(root, "descendant.pid")
	startSignal := filepath.Join(root, "start")
	telemetry := filepath.Join(root, "telemetry")
	fixture := filepath.Join(root, "plain.txt")
	resourceAction := "/bin/sleep 30"
	if mode == "output" {
		resourceAction = fmt.Sprintf("printf '%s'\nprintf '%s' >&2\n/bin/sleep 30", strings.Repeat("o", 700), strings.Repeat("e", 700))
	}
	script := fmt.Sprintf("#!/bin/sh\necho $$ > %s\n(while :; do /bin/sleep 1; done) &\necho $! > %s\nwhile [ ! -f %s ]; do /bin/sleep 0.01; done\n%s\n",
		strconv.Quote(rendererPID), strconv.Quote(descendantPID), strconv.Quote(startSignal),
		resourceAction)
	if err := os.WriteFile(filepath.Join(tools, "bat"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, []byte("native fallback must not run\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	controlled := map[string]string{
		"GO_WANT_PREVIEW_RESOURCE_HELPER": "1", "GO_PREVIEW_TOOLS": tools, "GO_PREVIEW_FIXTURE": fixture,
		"GO_PREVIEW_SIGNAL": startSignal, "GO_PREVIEW_TELEMETRY": telemetry, "GO_PREVIEW_RESOURCE_MODE": mode,
	}
	var starts, exits atomic.Int32
	runner := processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
		switch event.Phase {
		case "start":
			starts.Add(1)
		case "exit":
			exits.Add(1)
		}
	}}
	err := runner.Run(context.Background(), processpkg.Spec{Path: os.Args[0],
		Args: []string{"-test.run=^TestPreviewResourceHelperProcess$", "-test.v"},
		Env:  processpkg.SanitizeEnv(os.Environ(), controlled), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Containment: processpkg.ContainmentOwnTree, WaitDelay: time.Second})
	if err == nil || starts.Load() != 1 || exits.Load() != 1 {
		t.Fatalf("helper err=%v starts=%d exits=%d", err, starts.Load(), exits.Load())
	}
	data, readErr := os.ReadFile(telemetry)
	if readErr != nil || string(data) != "bat-started\n" {
		t.Fatalf("telemetry=%q err=%v", data, readErr)
	}
	assertProcessGone(t, rendererPID)
	assertProcessGone(t, descendantPID)
}

func TestPreviewResourceHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PREVIEW_RESOURCE_HELPER") != "1" {
		return
	}
	var stdout, stderr bytes.Buffer
	options := testOptions(&stdout)
	options.Stderr = &stderr
	options.Environment = []string{"PATH=" + os.Getenv("GO_PREVIEW_TOOLS")}
	if os.Getenv("GO_PREVIEW_RESOURCE_MODE") == "output" {
		options.Limits.MaxOutputBytes = 1000
	}
	options.Limits.Deadline = 5 * time.Second
	if os.Getenv("GO_PREVIEW_RESOURCE_MODE") == "deadline" {
		options.Limits.Deadline = 100 * time.Millisecond
	}
	options.OnDispatch = func(renderer string, _ int, duration time.Duration) {
		if duration != 0 {
			return
		}
		marker := os.Getenv("GO_PREVIEW_TELEMETRY")
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(file, "%s-started\n", renderer)
		_ = file.Close()
		if err := os.WriteFile(os.Getenv("GO_PREVIEW_SIGNAL"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := Render(context.Background(), resolved(os.Getenv("GO_PREVIEW_FIXTURE")), options)
	file, _ := os.OpenFile(os.Getenv("GO_PREVIEW_TELEMETRY"), os.O_WRONLY|os.O_APPEND, 0o600)
	if file != nil {
		_, _ = fmt.Fprintf(file, "returned:%v\n", err)
		_ = file.Close()
	}
	t.Fatalf("resource-limited inherited renderer returned: %v", err)
}

func assertProcessGone(t *testing.T, pidPath string) {
	t.Helper()
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d remains after inherited-group kill: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
