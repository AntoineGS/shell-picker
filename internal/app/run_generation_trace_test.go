package app

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestConcurrentQueuedTransitionTraceGenerationsAreUniqueAndTerminal(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.TracePath = filepath.Join(t.TempDir(), "generation.trace.jsonl")
	counter := filepath.Join(t.TempDir(), "count")
	fixture.dependencies.ZoxidePath = filepath.Join(t.TempDir(), "delayed-zoxide")
	if os.PathSeparator == '\\' {
		fixture.dependencies.ZoxidePath += ".exe"
	}
	build := exec.Command("go", "build", "-o", fixture.dependencies.ZoxidePath, "./testhelper/delayedzoxide")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build delayed zoxide: %v\n%s", err, output)
	}
	fixture.dependencies.Environment = append(fixture.dependencies.Environment,
		"GO_TEST_COUNTER="+counter, "GO_TEST_PATH="+fixture.cwd)

	started := make(chan struct{}, 3)
	var starts atomic.Int32
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		if event.Phase == "start" {
			starts.Add(1)
			started <- struct{}{}
		}
	}
	waitStarts := func(want int32) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for starts.Load() < want {
			select {
			case <-started:
			case <-deadline.C:
				t.Fatalf("process starts=%d want %d", starts.Load(), want)
			}
		}
	}
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		client := callbackClient(t, config)
		defer client.CloseIdleConnections()
		firstDone := make(chan error, 1)
		go func() {
			_, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
			firstDone <- err
		}()
		waitStarts(2)
		secondDone := make(chan sessionipc.EventResponse, 1)
		secondErr := make(chan error, 1)
		go func() {
			response, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
			secondDone <- response
			secondErr <- err
		}()
		waitStarts(3)
		if err := <-firstDone; err == nil {
			t.Fatal("retired transition unexpectedly succeeded")
		}
		response, err := <-secondDone, <-secondErr
		if err != nil || response.Effect.ReloadGeneration != 3 {
			t.Fatalf("replacement response=%+v err=%v", response, err)
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(fixture.options.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	startsByGeneration := make(map[uint64]int)
	terminalsByGeneration := make(map[uint64]int)
	var orderedStarts []uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		switch record.Event {
		case "generation.start":
			orderedStarts = append(orderedStarts, record.Generation)
			startsByGeneration[record.Generation]++
		case "generation.publish", "generation.discard":
			terminalsByGeneration[record.Generation]++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(orderedStarts) != 3 || orderedStarts[0] != 1 || orderedStarts[1] != 2 || orderedStarts[2] != 3 {
		t.Fatalf("generation starts=%v want [1 2 3]", orderedStarts)
	}
	for generation := uint64(1); generation <= 3; generation++ {
		if startsByGeneration[generation] != 1 || terminalsByGeneration[generation] != 1 {
			t.Fatalf("generation %d starts/terminal=%d/%d", generation, startsByGeneration[generation], terminalsByGeneration[generation])
		}
	}
}
