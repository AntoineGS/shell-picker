//go:build windows

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"golang.org/x/sys/windows"
)

func TestWindowsTracePipeAllowsSubsequentInstancesWithoutFirstInstanceFlag(t *testing.T) {
	name, first, err := createWindowsTracePipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(first) })
	second, err := createWindowsTracePipeInstance(name, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(second) })
	third, err := createWindowsTracePipeInstance(name, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(third) })
}

func TestWindowsTraceServerSignalsReadinessBeforeAnyClient(t *testing.T) {
	path, first, err := createWindowsTracePipe()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}
	session := &windowsTerminalSession{t: t, ops: defaultWindowsTerminalOps(), trace: first, tracePath: path,
		traceFactory: createWindowsTracePipeInstance, traceStarted: true, traceDone: make(chan struct{}),
		changed: make(chan struct{}), stop: make(chan struct{}), traceHandles: make(map[windows.Handle]struct{}),
		traceListeners: make(map[windows.Handle]struct{}), traceAcceptStop: make(chan struct{}),
		traceAcceptorsReady: make(chan struct{})}
	t.Cleanup(func() { _ = session.Close() })
	go session.drainTrace(first, make(chan struct{}))
	select {
	case <-session.traceAcceptorsReady:
	case <-time.After(2 * time.Second):
		t.Fatal("trace server did not become launch-ready without a client")
	}

	mainClient, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mainClient.Close() })
	callbackClient, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = callbackClient.Close() })
	mainTrace := integrationpkg.NewTrace(mainClient, sessionID)
	callbackTrace := integrationpkg.NewTrace(callbackClient, sessionID)
	if err := mainTrace.Event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"}); err != nil {
		t.Fatal(err)
	}
	if err := callbackTrace.Event(integrationpkg.TraceEvent{Name: "callback.info.start", Outcome: "started"}); err != nil {
		t.Fatal(err)
	}
	if err := callbackTrace.Event(integrationpkg.TraceEvent{Name: "callback.info", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := waitForWindowsTraceRecords(session, 1); err != nil {
		t.Fatal(err)
	}
	if err := mainClient.Close(); err != nil {
		t.Fatal(err)
	}
	if err := callbackClient.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	events := session.TraceEvents()
	if countWindowsTraceEvents(events, "session.start") != 1 ||
		countWindowsTraceEvents(events, "callback.info.start") != 1 ||
		countWindowsTraceEvents(events, "callback.info") != 1 {
		t.Fatalf("trace events=%+v", events)
	}
	if len(session.traceHandles) != 0 || len(session.traceListeners) != 0 || session.traceListener != 0 {
		t.Fatalf("trace resources retained: handles=%v listeners=%v listener=%d", session.traceHandles, session.traceListeners, session.traceListener)
	}
}

func TestWindowsTraceRecordsSortGloballyByEventTimeAfterReaderDrain(t *testing.T) {
	path, first, err := createWindowsTracePipe()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}
	session := &windowsTerminalSession{t: t, ops: defaultWindowsTerminalOps(), trace: first, tracePath: path,
		traceFactory: createWindowsTracePipeInstance, traceStarted: true, traceDone: make(chan struct{}),
		changed: make(chan struct{}), stop: make(chan struct{}), traceHandles: make(map[windows.Handle]struct{}),
		traceListeners: make(map[windows.Handle]struct{}), traceAcceptStop: make(chan struct{}),
		traceAcceptorsReady: make(chan struct{})}
	t.Cleanup(func() { _ = session.Close() })
	go session.drainTrace(first, make(chan struct{}))
	select {
	case <-session.traceAcceptorsReady:
	case <-time.After(2 * time.Second):
		t.Fatal("trace server did not become launch-ready without a client")
	}

	mainClient, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mainClient.Close() })
	pipeA, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipeA.Close() })
	pipeB, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipeB.Close() })
	mainTrace := integrationpkg.NewTrace(mainClient, sessionID)
	traceA := integrationpkg.NewTrace(pipeA, sessionID)
	traceB := integrationpkg.NewTrace(pipeB, sessionID)
	base := time.Now().UTC().Truncate(time.Nanosecond)
	write := func(trace *integrationpkg.Trace, name, outcome string, timestamp time.Time) {
		t.Helper()
		if err := trace.Event(integrationpkg.TraceEvent{Name: name, Outcome: outcome, Timestamp: timestamp}); err != nil {
			t.Fatal(err)
		}
	}
	write(mainTrace, "session.start", "cp", base)
	waitForWindowsTraceEvent(t, session, "session.start", "cp", 1)
	write(traceA, "callback.info", "ok", base.Add(2*time.Millisecond))
	waitForWindowsTraceEvent(t, session, "callback.info", "ok", 1)
	write(mainTrace, "session.close", "accepted", base.Add(3*time.Millisecond))
	waitForWindowsTraceEvent(t, session, "session.close", "accepted", 1)
	write(traceB, "callback.info.start", "started", base.Add(time.Millisecond))
	waitForWindowsTraceEvent(t, session, "callback.info.start", "started", 1)
	write(traceA, "callback.info.start", "started", base.Add(2*time.Millisecond))
	waitForWindowsTraceEvent(t, session, "callback.info.start", "started", 2)
	write(traceB, "callback.info", "ok", base.Add(2*time.Millisecond))
	waitForWindowsTraceEvent(t, session, "callback.info", "ok", 2)
	write(traceB, "callback.info.start", "started", base.Add(4*time.Millisecond))
	waitForWindowsTraceEvent(t, session, "callback.info.start", "started", 3)
	if err := mainClient.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pipeA.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pipeB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	events := session.TraceEvents()
	want := []struct {
		name, outcome, timestamp string
	}{
		{"session.start", "cp", base.Format(time.RFC3339Nano)},
		{"callback.info.start", "started", base.Add(time.Millisecond).Format(time.RFC3339Nano)},
		{"callback.info", "ok", base.Add(2 * time.Millisecond).Format(time.RFC3339Nano)},
		{"callback.info.start", "started", base.Add(2 * time.Millisecond).Format(time.RFC3339Nano)},
		{"callback.info", "ok", base.Add(2 * time.Millisecond).Format(time.RFC3339Nano)},
		{"session.close", "accepted", base.Add(3 * time.Millisecond).Format(time.RFC3339Nano)},
		{"callback.info.start", "started", base.Add(4 * time.Millisecond).Format(time.RFC3339Nano)},
	}
	if len(events) != len(want) {
		t.Fatalf("trace event count=%d events=%+v want=%d", len(events), events, len(want))
	}
	for index, expected := range want {
		got := events[index]
		if got.Event != expected.name || got.Outcome != expected.outcome || got.Time != expected.timestamp {
			t.Fatalf("trace event %d=%+v want name=%q outcome=%q time=%q", index, got, expected.name, expected.outcome, expected.timestamp)
		}
	}
	if _, err := countFirstFrameCallbackInvocations(events, true); err == nil {
		t.Fatal("post-close timestamp was accepted by first-frame validation")
	}
}

func TestWindowsTraceServerMergesConcurrentConnectionsWhileMainRemainsOpen(t *testing.T) {
	path, first, err := createWindowsTracePipe()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}
	session := &windowsTerminalSession{t: t, ops: defaultWindowsTerminalOps(), trace: first, tracePath: path,
		traceFactory: createWindowsTracePipeInstance, traceStarted: true, traceDone: make(chan struct{}),
		changed: make(chan struct{}), stop: make(chan struct{}), traceHandles: make(map[windows.Handle]struct{}),
		traceListeners:  make(map[windows.Handle]struct{}),
		traceAcceptStop: make(chan struct{}), traceAcceptorsReady: make(chan struct{})}
	t.Cleanup(func() { _ = session.Close() })
	ready := make(chan struct{})
	go session.drainTrace(first, ready)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("trace server did not begin listening")
	}

	mainClient, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mainClient.Close() })
	mainTrace := integrationpkg.NewTrace(mainClient, sessionID)
	if err := mainTrace.Event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.traceAcceptorsReady:
	case <-time.After(2 * time.Second):
		t.Fatal("trace acceptors did not become ready")
	}

	const callbackCount = 4
	var wait sync.WaitGroup
	errorsCh := make(chan error, callbackCount)
	for index := 0; index < callbackCount; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			client, err := openWindowsTraceClient(path)
			if err != nil {
				errorsCh <- err
				return
			}
			trace := integrationpkg.NewTrace(client, sessionID)
			if err := trace.Event(integrationpkg.TraceEvent{Name: "callback.info.start", Outcome: "started"}); err != nil {
				errorsCh <- err
				_ = client.Close()
				return
			}
			if err := trace.Event(integrationpkg.TraceEvent{Name: "callback.info", Outcome: "ok"}); err != nil {
				errorsCh <- err
				_ = client.Close()
				return
			}
			if err := client.Close(); err != nil {
				errorsCh <- fmt.Errorf("callback %d close: %w", index, err)
			}
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if err := mainTrace.Event(integrationpkg.TraceEvent{Name: "generation.start", Generation: 1, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := mainClient.Close(); err != nil {
		t.Fatal(err)
	}

	if err := waitForWindowsTraceRecords(session, callbackCount); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if len(session.traceHandles) != 0 || len(session.traceListeners) != 0 || session.traceListener != 0 {
		t.Fatalf("trace resources retained: handles=%v listeners=%v listener=%d", session.traceHandles, session.traceListeners, session.traceListener)
	}
	events := session.TraceEvents()
	if countWindowsTraceEvents(events, "session.start") != 1 || countWindowsTraceEvents(events, "generation.start") != 1 ||
		countWindowsTraceEvents(events, "callback.info.start") != callbackCount || countWindowsTraceEvents(events, "callback.info") != callbackCount {
		t.Fatalf("trace events=%+v", events)
	}
	if countWindowsTraceOutcomes(events, "session.start", "cp") != 1 || countWindowsTraceOutcomes(events, "generation.start", "ok") != 1 ||
		countWindowsTraceOutcomes(events, "callback.info.start", "started") != callbackCount ||
		countWindowsTraceOutcomes(events, "callback.info", "ok")+countWindowsTraceOutcomes(events, "callback.info", "error") != callbackCount {
		t.Fatalf("trace outcomes=%+v", events)
	}
}

func TestWindowsTraceServerAcceptsRealCallbacksSequentiallyAndConcurrently(t *testing.T) {
	picker, _ := cachedRealBinaries(t)
	path, first, err := createWindowsTracePipe()
	if err != nil {
		t.Fatal(err)
	}
	sessionID := [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}
	session := &windowsTerminalSession{t: t, ops: defaultWindowsTerminalOps(), trace: first, tracePath: path,
		traceFactory: createWindowsTracePipeInstance, traceStarted: true, traceDone: make(chan struct{}),
		changed: make(chan struct{}), stop: make(chan struct{}), traceHandles: make(map[windows.Handle]struct{}),
		traceListeners: make(map[windows.Handle]struct{}), traceAcceptStop: make(chan struct{}),
		traceAcceptorsReady: make(chan struct{})}
	t.Cleanup(func() { _ = session.Close() })
	ready := make(chan struct{})
	go session.drainTrace(first, ready)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("trace server did not begin listening")
	}
	mainClient, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mainClient.Close()
	mainTrace := integrationpkg.NewTrace(mainClient, sessionID)
	if err := mainTrace.Event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.traceAcceptorsReady:
	case <-time.After(2 * time.Second):
		t.Fatal("trace acceptors did not become ready")
	}

	environment := replaceEnvironment(os.Environ(),
		"SHELL_PICKER_TRACE_PATH="+path,
		"SHELL_PICKER_TRACE_SESSION="+integrationpkg.RedactedSessionID(sessionID),
		"SHELL_PICKER_ADDR=", "SHELL_PICKER_TOKEN=",
		"FZF_MATCH_COUNT=7", "FZF_TOTAL_COUNT=42", "FZF_SELECT_COUNT=1")
	runCallback := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, picker, "--fzf-shell", "i:cp")
		command.Env = environment
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("callback command: %w; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		if got := stdout.String(); got != "7/42 (1)" {
			return fmt.Errorf("callback stdout=%q, want %q", got, "7/42 (1)")
		}
		if got := stderr.String(); got != "" {
			return fmt.Errorf("callback stderr=%q", got)
		}
		return nil
	}
	for range 2 {
		if err := runCallback(); err != nil {
			t.Fatal(err)
		}
	}
	const concurrentCallbacks = 4
	errorsCh := make(chan error, concurrentCallbacks)
	var callbacks sync.WaitGroup
	for range concurrentCallbacks {
		callbacks.Add(1)
		go func() {
			defer callbacks.Done()
			errorsCh <- runCallback()
		}()
	}
	callbacks.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := mainTrace.Event(integrationpkg.TraceEvent{Name: "generation.start", Generation: 1, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := waitForWindowsTraceRecords(session, 2+concurrentCallbacks); err != nil {
		t.Fatal(err)
	}
	if err := mainClient.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	events := session.TraceEvents()
	if countWindowsTraceEvents(events, "session.start") != 1 || countWindowsTraceEvents(events, "generation.start") != 1 ||
		countWindowsTraceEvents(events, "callback.info.start") != 2+concurrentCallbacks ||
		countWindowsTraceEvents(events, "callback.info") != 2+concurrentCallbacks ||
		countWindowsTraceEvents(events, "trace.error") != 0 {
		t.Fatalf("trace events=%+v", events)
	}
	if countWindowsTraceOutcomes(events, "session.start", "cp") != 1 || countWindowsTraceOutcomes(events, "generation.start", "ok") != 1 ||
		countWindowsTraceOutcomes(events, "callback.info.start", "started") != 2+concurrentCallbacks ||
		countWindowsTraceOutcomes(events, "callback.info", "ok") != 2+concurrentCallbacks {
		t.Fatalf("trace outcomes=%+v", events)
	}
	if len(session.traceHandles) != 0 || len(session.traceListeners) != 0 || session.traceListener != 0 {
		t.Fatalf("trace resources retained: handles=%v listeners=%v listener=%d", session.traceHandles, session.traceListeners, session.traceListener)
	}
}

func TestWindowsTraceServerCloseCancelsAndClosesAllInstancesOnce(t *testing.T) {
	path, first, err := createWindowsTracePipe()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	serverHandles := []windows.Handle{first}
	serverLive := map[windows.Handle]struct{}{first: {}}
	closed := make(map[windows.Handle]int)
	factory := func(name string, firstInstance bool) (windows.Handle, error) {
		handle, err := createWindowsTracePipeInstance(name, firstInstance)
		if err == nil {
			mu.Lock()
			serverHandles = append(serverHandles, handle)
			serverLive[handle] = struct{}{}
			mu.Unlock()
		}
		return handle, err
	}
	ops := defaultWindowsTerminalOps()
	ops.closeHandle = func(handle windows.Handle) error {
		mu.Lock()
		if _, ok := serverLive[handle]; ok {
			closed[handle]++
			delete(serverLive, handle)
		}
		mu.Unlock()
		return windows.CloseHandle(handle)
	}
	session := &windowsTerminalSession{t: t, ops: ops, trace: first, tracePath: path,
		traceFactory: factory, traceStarted: true, traceDone: make(chan struct{}),
		changed: make(chan struct{}), stop: make(chan struct{}), traceHandles: make(map[windows.Handle]struct{}),
		traceListeners: make(map[windows.Handle]struct{}), traceAcceptStop: make(chan struct{}),
		traceAcceptorsReady: make(chan struct{}), cleanupTimeout: 10 * time.Millisecond}
	t.Cleanup(func() { _ = session.Close() })
	ready := make(chan struct{})
	go session.drainTrace(first, ready)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("trace server did not begin listening")
	}
	client, err := openWindowsTraceClient(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-session.traceAcceptorsReady:
	case <-time.After(2 * time.Second):
		t.Fatal("trace acceptors did not become ready")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, handle := range serverHandles {
		if closed[handle] != 1 {
			t.Errorf("trace handle %d closes=%d, want one", handle, closed[handle])
		}
	}
	if len(session.traceHandles) != 0 || len(session.traceListeners) != 0 || session.traceListener != 0 {
		t.Fatalf("trace resources retained: handles=%v listeners=%v listener=%d", session.traceHandles, session.traceListeners, session.traceListener)
	}
}

func countWindowsTraceEvents(events []traceEvent, name string) int {
	count := 0
	for _, event := range events {
		if event.Event == name {
			count++
		}
	}
	return count
}

func countWindowsTraceOutcomes(events []traceEvent, name, outcome string) int {
	count := 0
	for _, event := range events {
		if event.Event == name && event.Outcome == outcome {
			count++
		}
	}
	return count
}
