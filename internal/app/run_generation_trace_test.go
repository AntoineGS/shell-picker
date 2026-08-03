package app

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestTransitionTraceGenerationsAreUniqueAndTerminal(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
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

	var starts atomic.Int32
	zoxideStarted := make(chan struct{})
	releaseZoxide := make(chan struct{})
	var zoxideStartedOnce, releaseZoxideOnce sync.Once
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		if event.Phase == "start" && event.Path == fixture.dependencies.ZoxidePath {
			starts.Add(1)
			zoxideStartedOnce.Do(func() { close(zoxideStarted) })
			<-releaseZoxide
		}
	}
	fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
		defer releaseZoxideOnce.Do(func() { close(releaseZoxide) })
		client := callbackClient(t, config)
		defer client.CloseIdleConnections()
		select {
		case <-zoxideStarted:
		case <-ctx.Done():
			return fzf.Result{}, context.Cause(ctx)
		}
		for generation := uint64(2); generation <= 3; generation++ {
			response, err := client.Event(ctx, sessionipc.EventRequest{Opcode: protocol.OpParent, Key: "left"})
			if err != nil || response.Effect.ReloadGeneration != generation {
				t.Fatalf("generation %d response=%+v err=%v", generation, response, err)
			}
			finalizeTestEvent(t, ctx, client, response, true)
			if _, err := client.Load(ctx, sessionipc.LoadRequest{Generation: response.Effect.ReloadGeneration, EventID: response.EventID}); err != nil {
				t.Fatalf("generation %d load: %v", generation, err)
			}
			finalizeTestLoad(t, ctx, client, response.EventID, true)
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
	publicationsByGeneration := make(map[uint64]integrationpkg.TraceRecord)
	enrichmentTerminals := 0
	var enrichmentTerminal integrationpkg.TraceRecord
	var initialPublication integrationpkg.TraceRecord
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
		case "generation.publish":
			publicationsByGeneration[record.Generation] = record
			terminalsByGeneration[record.Generation]++
		case "generation.discard":
			terminalsByGeneration[record.Generation]++
		case "zoxide.enrichment":
			enrichmentTerminals++
			enrichmentTerminal = record
			if record.Generation == 0 {
				t.Fatalf("zoxide enrichment has zero generation: %+v", record)
			}
		}
		if record.Event == "generation.publish" && record.Generation == 1 {
			initialPublication = record
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
	for generation := uint64(2); generation <= 3; generation++ {
		publication, ok := publicationsByGeneration[generation]
		if !ok || publication.ZoxideOutcome != "not-run" || publication.ZoxideAttempts != 0 ||
			publication.ZoxideStarts != 0 || publication.ZoxideProcesses != 0 {
			t.Fatalf("generation %d publication=%+v present=%v", generation, publication, ok)
		}
	}
	if initialPublication.ZoxidePolicy != "fresh" || initialPublication.ZoxideOutcome != "pending" ||
		initialPublication.ZoxideAttempts != 0 || initialPublication.ZoxideStarts != 0 ||
		initialPublication.ZoxideExits != 0 || initialPublication.ZoxideProcesses != 0 || initialPublication.ZoxideUS != 0 {
		t.Fatalf("initial publication=%+v", initialPublication)
	}
	if enrichmentTerminals != 1 {
		t.Fatalf("zoxide enrichment terminals=%d", enrichmentTerminals)
	}
	if enrichmentTerminal.Outcome != "discarded" || enrichmentTerminal.Generation != 1 || enrichmentTerminal.CandidateCount != 0 {
		t.Fatalf("enrichment terminal=%+v", enrichmentTerminal)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("helper process starts=%d want 1", got)
	}
}
