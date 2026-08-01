//go:build linux

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func (tree observedPreviewTree) close() {
	for _, fd := range []int{tree.CallbackFD, tree.RendererFD, tree.GrandchildFD} {
		_ = unix.Close(fd)
	}
}

func linuxParentPID(pid int) (int, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, errors.New("malformed proc stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 2 {
		return 0, errors.New("short proc stat")
	}
	return strconv.Atoi(fields[1])
}

func assertRealSessionProcessTree(t *testing.T, fixture *blockingPreviewFixture, term terminalSession, tree observedPreviewTree) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[int]linuxProcessNode)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		ppid, err := linuxParentPID(pid)
		if err != nil {
			continue
		}
		exe, err := os.Readlink("/proc/" + entry.Name() + "/exe")
		if err != nil {
			continue
		}
		rawArgs, _ := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		fields := strings.Split(strings.TrimSuffix(string(rawArgs), "\x00"), "\x00")
		nodes[pid] = linuxProcessNode{pid: pid, ppid: ppid, exe: exe, args: fields}
	}
	descendants := map[int]bool{term.PID(): true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid] = true
				changed = true
			}
		}
	}
	wantFZF, err := filepath.EvalSymlinks(fixture.fzf)
	if err != nil {
		t.Fatal(err)
	}
	fzfCount := 0
	for pid := range descendants {
		if pid == term.PID() {
			continue
		}
		node := nodes[pid]
		if node.exe == wantFZF {
			fzfCount++
			if node.ppid != term.PID() {
				t.Fatalf("fzf pid %d parent=%d want picker %d", pid, node.ppid, term.PID())
			}
		}
		base := strings.ToLower(filepath.Base(node.exe))
		if map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "cmd.exe": true, "powershell.exe": true}[base] {
			t.Fatalf("interpreter in real picker tree: %+v", node)
		}
		for _, argument := range node.args {
			if argument == "--listen" || strings.HasPrefix(argument, "--listen=") || strings.Contains(argument, "SHELL_PICKER_TOKEN") {
				t.Fatalf("listener or callback credential in process argv: %+v", node)
			}
		}
	}
	if fzfCount != 1 || !descendants[tree.CallbackPID] || !descendants[tree.RendererPID] || !descendants[tree.GrandchildPID] {
		t.Fatalf("process tree fzf=%d callback/renderer/grandchild=%v/%v/%v", fzfCount,
			descendants[tree.CallbackPID], descendants[tree.RendererPID], descendants[tree.GrandchildPID])
	}
}

func waitTreeExit(ctx context.Context, tree observedPreviewTree) error {
	polls := []unix.PollFd{{Fd: int32(tree.CallbackFD), Events: unix.POLLIN}, {Fd: int32(tree.RendererFD), Events: unix.POLLIN},
		{Fd: int32(tree.GrandchildFD), Events: unix.POLLIN}}
	for {
		remaining := time.Until(deadline(ctx))
		if remaining <= 0 {
			return ctx.Err()
		}
		timeout := unix.NsecToTimespec(remaining.Nanoseconds())
		if _, err := unix.Ppoll(polls, &timeout, nil); err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		all := true
		for _, poll := range polls {
			all = all && poll.Revents != 0
		}
		if all {
			return nil
		}
	}
}

func deadline(ctx context.Context) time.Time {
	value, _ := ctx.Deadline()
	return value
}

func TestRealFZFPreviewReplacementKillsWholeTree(t *testing.T) {
	fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
	term := fixture.Start(t)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	first := fixture.waitTree(t, 1)
	defer first.close()
	assertRealSessionProcessTree(t, fixture, term, first)
	if events := fixture.controller.snapshot(); len(events) != 2 {
		t.Fatalf("steady preview tree events=%+v, want one renderer and one grandchild", events)
	}
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	second := fixture.waitTree(t, 2)
	defer second.close()
	if first.RendererPID == second.RendererPID || first.CallbackPID == second.CallbackPID {
		t.Fatalf("preview replacement reused process identity: first=%+v second=%+v", first, second)
	}
	if events := fixture.controller.snapshot(); len(events) > 4 {
		t.Fatalf("replacement exceeded two renderer trees: %+v", events)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitTreeExit(ctx, first); err != nil {
		t.Fatalf("old callback/renderer/grandchild did not exit: %v", err)
	}
	for _, event := range fixture.controller.snapshot() {
		if event.Event == "renderer-exit" && event.PID == first.RendererPID {
			t.Fatalf("killed renderer claimed normal completion: %+v", event)
		}
	}
	assertPreviewTraceCount(t, term.TraceEvents(), "preview.dispatch", "eza", "ok", 2)
	if err := fixture.controller.release(second.RendererPID); err != nil {
		t.Fatal(err)
	}
	finished := fixture.controller.wait(testContext(t), "renderer-exit", 1)
	if finished.PID != second.RendererPID {
		t.Fatalf("renderer exit=%+v want pid %d", finished, second.RendererPID)
	}
	if err := waitTreeExit(testContext(t), second); err != nil {
		t.Fatalf("released preview tree did not exit: %v", err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: 1})
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	assertFinishedTrace(t, term.TraceEvents(), "eza", 1)
}

func TestRealFZFResizeUpdatesPreviewDimensions(t *testing.T) {
	fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	ldflags := "-X=main.helperPath=" + fixture.helper + " -X=main.controller=" + fixture.controller.address() + " -X=main.nonce=" + fixture.controller.nonce
	buildCommand(t, repository, filepath.Join(fixture.fakeBin, "chafa"), "-ldflags", ldflags, "./integration/testhelper/delegate")
	for _, name := range []string{"image-a.png", "image-b.png", "image-c.png"} {
		if err := os.WriteFile(filepath.Join(fixture.cwd, name), []byte("\x89PNG\r\n\x1a\nfixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	term := fixture.Start(t)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	initial := fixture.waitTree(t, 1)
	defer initial.close()
	if err := term.Send([]byte("image")); err != nil {
		t.Fatal(err)
	}
	first := fixture.waitTree(t, 2)
	defer first.close()
	if err := waitTreeExit(testContext(t), initial); err != nil {
		t.Fatalf("initial directory preview tree did not exit: %v", err)
	}
	beforeResize := len(term.Output())
	if err := term.Resize(101, 37); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeResize)
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	second := fixture.waitTree(t, 3)
	defer second.close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Renderer: "chafa", Operation: "ok", Count: 2})
	beforeResize = len(term.Output())
	if err := term.Resize(83, 29); err != nil {
		t.Fatal(err)
	}
	term.WaitOutputAfter(testContext(t), beforeResize)
	if err := term.Send([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatal(err)
	}
	third := fixture.waitTree(t, 4)
	defer third.close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Renderer: "chafa", Operation: "ok", Count: 3})
	assertStackedPreviewDimensions(t, first, 120, 35)
	assertStackedPreviewDimensions(t, second, 101, 37)
	assertStackedPreviewDimensions(t, third, 83, 29)
	assertPreviewTraceCount(t, term.TraceEvents(), "preview.dispatch", "chafa", "ok", 3)
	if err := fixture.controller.release(third.RendererPID); err != nil {
		t.Fatal(err)
	}
	finished := fixture.controller.wait(testContext(t), "renderer-exit", 1)
	if finished.PID != third.RendererPID {
		t.Fatalf("renderer exit=%+v want pid %d", finished, third.RendererPID)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "chafa", Count: 1})
	for _, tree := range []observedPreviewTree{initial, first, second, third} {
		if err := waitTreeExit(testContext(t), tree); err != nil {
			t.Fatalf("preview tree %+v did not exit: %v", tree, err)
		}
	}
	if err := term.Send([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	assertFinishedTrace(t, term.TraceEvents(), "chafa", 1)
}

func assertStackedPreviewDimensions(t *testing.T, tree observedPreviewTree, columns, lines int) {
	t.Helper()
	wantColumns, wantLines := columns-4, (lines-4)/2
	if tree.Columns != wantColumns || tree.Lines != wantLines {
		t.Fatalf("stacked preview dimensions=%dx%d for terminal %dx%d, want %dx%d",
			tree.Columns, tree.Lines, columns, lines, wantColumns, wantLines)
	}
}

func TestRealFZFPreviewTerminalFailuresKillWholeTree(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  string
		bound time.Duration
	}{
		{name: "deadline", mode: "renderer", bound: 15 * time.Second},
		{name: "output-overflow", mode: "overflow-renderer", bound: 5 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
			fixture.setRendererMode(t, test.mode)
			term := fixture.Start(t)
			defer term.Close()
			term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
			term.AssertProcessTopology(t)
			tree := fixture.waitTree(t, 1)
			defer tree.close()
			ctx, cancel := context.WithTimeout(context.Background(), test.bound)
			defer cancel()
			if err := waitTreeExit(ctx, tree); err != nil {
				t.Fatalf("terminal preview tree survived: %v", err)
			}
			controllerEvents := fixture.controller.snapshot()
			if len(controllerEvents) != 2 || controllerEvents[0].Event != "renderer-started" ||
				controllerEvents[1].Event != "grandchild-started" {
				t.Fatalf("preview child budget events=%+v, want one sequential renderer and one grandchild tree", controllerEvents)
			}
			for _, event := range controllerEvents {
				if event.Event == "renderer-exit" {
					t.Fatalf("terminal renderer claimed normal completion: %+v", event)
				}
			}
			sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
			if err := term.Send([]byte("q")); err != nil {
				t.Fatal(err)
			}
			if err := term.Wait(testContext(t)); err != nil {
				t.Fatal(err)
			}
			term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
			assertPreviewTraceCount(t, term.TraceEvents(), "preview.dispatch", "eza", "ok", 1)
			assertFinishedTrace(t, term.TraceEvents(), "eza", 0)
		})
	}
}
