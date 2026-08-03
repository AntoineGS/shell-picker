package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/preview"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

var errTask20Cancellation = errors.New("task20 cancellation")

type ownedProcessIdentity interface {
	PID() int
	WaitGone(context.Context) error
	Close() error
}

func runTask20ResourceIterations(t *testing.T, iterations int) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("owned process identity resource gate requires Linux pidfds or Windows process handles")
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	for _, path := range []string{os.Getenv("TMPDIR"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("LOCALAPPDATA")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tools := filepath.Join(root, "tools")
	buildTask20IntegrationHelper(t, tools)
	if err := os.WriteFile(filepath.Join(tools, "block"), []byte("block\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("task20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(root, "cache")
	if _, err := preview.NewCache(cacheRoot, 8<<20); err != nil {
		t.Fatal(err)
	}

	// One complete lifecycle warms the Go poller and process/runtime descriptors
	// before the exact baseline. Its evidence is removed through owned paths.
	runTask20CancelledNavigation(t, root)
	runTask20CancelledPreviewHandler(t)
	runTask20CancelledExternalPreview(t, tools, fixture, cacheRoot)
	removeTask20Evidence(t, tools)
	baseline := snapshotResources(t, root)

	for range iterations {
		runTask20CancelledNavigation(t, root)
		runTask20CancelledPreviewHandler(t)
		runTask20CancelledExternalPreview(t, tools, fixture, cacheRoot)
		removeTask20Evidence(t, tools)
	}
	awaitResourcesReturned(t, baseline, 3*time.Second, root)
}

func runTask20CancelledNavigation(t *testing.T, ownedRoot string) {
	t.Helper()
	base, err := os.MkdirTemp(ownedRoot, "navigation-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	target := filepath.Join(base, "child")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	record := candidate.Record{Kind: protocol.KindDirectory, Display: "child", Path: []byte(target),
		Payload: protocol.EncodePath([]byte(target)), Target: pathutil.Filesystem([]byte(target))}
	started, returned := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	actor := session.New(context.Background(), func(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		switch calls.Add(1) {
		case 1:
			return candidate.BuildResult{Records: []candidate.Record{record}}, nil
		case 2:
			if string(request.Location.Path) != target {
				return candidate.BuildResult{}, fmt.Errorf("navigation target mismatch")
			}
			close(started)
			<-ctx.Done()
			close(returned)
			return candidate.BuildResult{}, context.Cause(ctx)
		default:
			return candidate.BuildResult{}, errors.New("unexpected generation")
		}
	})
	initial, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert,
			Location: pathutil.Filesystem([]byte(base)), Home: pathutil.Filesystem([]byte(base))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte(base)), Initial: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	handleDone := make(chan error, 1)
	go func() {
		_, handleErr := session.Handle(ctx, actor, protocol.Event{Opcode: protocol.OpForward, CurrentItem: record.Wire().Bytes()})
		handleDone <- handleErr
	}()
	deadline, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	awaitTask20Signal(t, deadline, started, "navigation generation start")
	old, err := actor.Current(deadline)
	if err != nil || old.Generation() != initial.Snapshot.Generation() || old.State().Location.Kind != pathutil.KindFilesystem ||
		string(old.State().Location.Path) != base || len(old.Records()) != 1 {
		t.Fatalf("responsive old snapshot changed during navigation: generation=%d err=%v", old.Generation(), err)
	}
	cause := errors.New("cancel task20 navigation")
	cancel(cause)
	awaitTask20Signal(t, deadline, returned, "navigation generator return")
	select {
	case err := <-handleDone:
		if !errors.Is(err, cause) {
			t.Fatalf("navigation error=%v", err)
		}
	case <-deadline.Done():
		t.Fatal("session.Handle did not join")
	}
	if calls.Load() != 2 {
		t.Fatalf("generation calls=%d", calls.Load())
	}
	after, err := actor.Current(deadline)
	if err != nil || after.Generation() != old.Generation() || string(after.State().Location.Path) != base || len(after.Records()) != 1 {
		t.Fatalf("cancelled navigation published partial state: generation=%d err=%v", after.Generation(), err)
	}
	if err := actor.Close(); err != nil {
		t.Fatal(err)
	}
}

type task20BlockingBackend struct {
	started, returned chan struct{}
	entered, exited   atomic.Int32
	once              sync.Once
}

func (backend *task20BlockingBackend) HandleEvent(context.Context, protocol.Event) (sessionipc.EventResult, error) {
	return sessionipc.EventResult{}, nil
}
func (backend *task20BlockingBackend) LoadGeneration(context.Context, sessionipc.LoadRequest) ([]byte, error) {
	return nil, nil
}
func (backend *task20BlockingBackend) CurrentHeader(context.Context) (string, error) {
	return "", nil
}
func (backend *task20BlockingBackend) RecordPreview(context.Context, sessionipc.PreviewRequest) error {
	return nil
}
func (backend *task20BlockingBackend) ResolvePreview(ctx context.Context, _ []byte) (protocol.ResolvedCandidate, error) {
	backend.entered.Add(1)
	backend.once.Do(func() { close(backend.started) })
	<-ctx.Done()
	backend.exited.Add(1)
	close(backend.returned)
	return protocol.ResolvedCandidate{}, context.Cause(ctx)
}

func runTask20CancelledPreviewHandler(t *testing.T) {
	t.Helper()
	token, err := sessionipc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	backend := &task20BlockingBackend{started: make(chan struct{}), returned: make(chan struct{})}
	handleScope := beginTask20HandleScope(t, "server")
	server, err := sessionipc.Listen(context.Background(), token, backend)
	if err != nil {
		t.Fatal(err)
	}
	handleScope.Capture(t)
	client, err := sessionipc.NewClientFromEnv(func(key string) string {
		if key == "SHELL_PICKER_ADDR" {
			return server.Address()
		}
		if key == "SHELL_PICKER_TOKEN" {
			return token.String()
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		_, requestErr := client.ResolvePreview(context.Background(), sessionipc.PreviewRequest{
			Phase: "resolve", CurrentItemBase64: base64.StdEncoding.EncodeToString([]byte("current"))})
		requestDone <- requestErr
	}()
	deadline, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	awaitTask20Signal(t, deadline, backend.started, "IPC backend entry")
	if err := server.Close(deadline); err != nil {
		t.Fatal(err)
	}
	awaitTask20Signal(t, deadline, backend.returned, "IPC backend return")
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("cancelled IPC request succeeded")
		}
	case <-deadline.Done():
		t.Fatal("IPC request goroutine did not join")
	}
	if backend.entered.Load() != 1 || backend.exited.Load() != 1 {
		t.Fatalf("IPC handlers entered=%d returned=%d", backend.entered.Load(), backend.exited.Load())
	}
	if _, err := client.Load(deadline, sessionipc.LoadRequest{Generation: 1}); err == nil {
		t.Fatal("closed listener accepted a post-close request")
	}
	client.CloseIdleConnections()
	handleScope.RequireClosed(t)
}

func runTask20CancelledExternalPreview(t *testing.T, tools, fixture, cacheRoot string) {
	t.Helper()
	processLog := filepath.Join(tools, "processes.log")
	environmentLog := filepath.Join(tools, "environment.log")
	_ = os.Remove(processLog)
	_ = os.Remove(environmentLog)
	readCancel, writeCancel, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readCancel.Close()
	defer writeCancel.Close()
	handleScope := beginTask20HandleScope(t, "process/job")
	events := make(chan process.ProcessEvent, 4)
	runner := process.Runner{Observe: func(event process.ProcessEvent) { events <- event }}
	done := make(chan error, 1)
	var output bytes.Buffer
	go func() {
		done <- runner.Run(context.Background(), process.Spec{Path: os.Args[0],
			Args: []string{"-test.run=^TestTask20PreviewResourceHelper$"}, Stdin: readCancel,
			Stdout: &output, Stderr: &output, Containment: process.ContainmentOwnTree, WaitDelay: time.Second,
			Env: process.SanitizeEnv(os.Environ(), map[string]string{"TASK20_PREVIEW_HELPER": "1",
				"TASK20_PREVIEW_TOOLS": tools, "TASK20_PREVIEW_FIXTURE": fixture, "TASK20_PREVIEW_CACHE": cacheRoot})})
	}()
	deadline, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	attempt, start := awaitTask20ProcessStart(t, deadline, events, done)
	handleScope.Capture(t)
	outer, err := openOwnedProcessIdentity(start.PID)
	if err != nil {
		t.Fatalf("open outer process identity: %v", err)
	}
	if err := verifyOwnedProcessGroup(start.PID); err != nil {
		_ = outer.Close()
		t.Fatalf("verify owned process group: %v", err)
	}
	identities := []ownedProcessIdentity{outer}
	innerPIDs := awaitTask20ProcessJournal(t, deadline, processLog)
	for _, pid := range innerPIDs {
		identity, err := openOwnedProcessIdentity(pid)
		if err != nil {
			t.Fatalf("open child process identity: %v", err)
		}
		identities = append(identities, identity)
	}
	if _, err := writeCancel.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := writeCancel.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled preview helper succeeded")
		}
	case <-deadline.Done():
		t.Fatalf("preview helper did not exit: %s", output.String())
	}
	exit := awaitTask20Exit(t, deadline, events)
	if attempt.Phase != "attempt" || start.PID <= 0 || exit.PID != start.PID {
		t.Fatalf("outer lifecycle attempt=%+v start=%+v exit=%+v", attempt, start, exit)
	}
	for _, identity := range identities {
		if err := identity.WaitGone(deadline); err != nil {
			t.Fatalf("process identity %d remained: %v", identity.PID(), err)
		}
		if err := identity.Close(); err != nil {
			t.Fatalf("close process identity: %v", err)
		}
	}
	assertTask20CacheEmpty(t, cacheRoot)
	handleScope.RequireClosed(t)
}

func TestTask20PreviewResourceHelper(t *testing.T) {
	if os.Getenv("TASK20_PREVIEW_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		var signal [1]byte
		_, _ = io.ReadFull(os.Stdin, signal[:])
		cancel(errTask20Cancellation)
	}()
	cache, err := preview.NewCache(os.Getenv("TASK20_PREVIEW_CACHE"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	fixture := os.Getenv("TASK20_PREVIEW_FIXTURE")
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	err = preview.Render(ctx, protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(fixture), Size: info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}, preview.Options{
		Environment: []string{"PATH=" + os.Getenv("TASK20_PREVIEW_TOOLS")}, Runner: process.Runner{}, Cache: cache,
		Stdout: io.Discard, Stderr: io.Discard})
	if !errors.Is(err, errTask20Cancellation) {
		t.Fatalf("preview cancellation=%v", err)
	}
}

func buildTask20IntegrationHelper(t *testing.T, tools string) {
	t.Helper()
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "bat"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", filepath.Join(tools, name), "./integration/testhelper/resource")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build preview resource helper: %v: %s", err, output)
	}
}

func awaitTask20Signal(t *testing.T, ctx context.Context, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitTask20ProcessStart(t *testing.T, ctx context.Context, events <-chan process.ProcessEvent,
	done <-chan error) (process.ProcessEvent, process.ProcessEvent) {
	t.Helper()
	var attempt process.ProcessEvent
	for {
		select {
		case event := <-events:
			if event.Phase == "attempt" {
				attempt = event
			}
			if event.Phase == "start" {
				return attempt, event
			}
		case err := <-done:
			t.Fatalf("preview helper returned before start: %v", err)
		case <-ctx.Done():
			t.Fatal("preview helper did not start")
		}
	}
}

func awaitTask20Exit(t *testing.T, ctx context.Context, events <-chan process.ProcessEvent) process.ProcessEvent {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Phase == "exit" {
				return event
			}
		case <-ctx.Done():
			t.Fatal("preview helper emitted no exit event")
		}
	}
}

func awaitTask20ProcessJournal(t *testing.T, ctx context.Context, path string) []int {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Fields(string(raw))
			if len(lines) == 4 && lines[0] == "renderer" && lines[2] == "grandchild" {
				first, firstErr := strconv.Atoi(lines[1])
				second, secondErr := strconv.Atoi(lines[3])
				if firstErr == nil && secondErr == nil && first > 0 && second > 0 && first != second {
					return []int{first, second}
				}
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("renderer/grandchild identities were not published")
		}
	}
}

func assertTask20CacheEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		t.Fatalf("preview cache/temp root retained entries %v", names)
	}
}

func removeTask20Evidence(t *testing.T, tools string) {
	t.Helper()
	for _, name := range []string{"environment.log", "processes.log"} {
		if err := os.Remove(filepath.Join(tools, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}
