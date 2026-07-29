//go:build linux

package integration

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/preview"
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type previewGolden struct {
	Categories                map[string]string `json:"categories"`
	Variants                  []string          `json:"variants"`
	MaximumLiveChildren       int               `json:"maximum_live_children"`
	MaximumSequentialChildren int               `json:"maximum_sequential_children"`
	IntentionalFixes          []string          `json:"intentional_fixes"`
}

type parityToolLog struct {
	Mode string   `json:"mode"`
	PID  int      `json:"pid"`
	Args []string `json:"args"`
}

func TestPreviewCategoryMatrix(t *testing.T) {
	golden := loadParityGolden[previewGolden](t, "preview.json")
	wantVariants := []string{"present", "missing", "failure", "hanging", "overflow"}
	if !reflect.DeepEqual(golden.Variants, wantVariants) || len(golden.Categories) != 13 ||
		golden.MaximumLiveChildren != 1 || golden.MaximumSequentialChildren != 3 || len(golden.IntentionalFixes) != 3 {
		t.Fatalf("preview golden=%+v", golden)
	}
	for category, chain := range golden.Categories {
		category, chain := category, chain
		for _, variant := range golden.Variants {
			variant := variant
			t.Run(category+"/"+variant, func(t *testing.T) {
				path := writeParityPreviewFixture(t, category)
				if !filepath.IsAbs(path) {
					t.Fatalf("fixture is not absolute: %q", path)
				}
				tools := t.TempDir()
				logPath := filepath.Join(t.TempDir(), "tools.jsonl")
				mode := map[string]string{"present": "tool-success", "failure": "tool-fail", "hanging": "tool-hang", "overflow": "tool-overflow"}[variant]
				if variant != "missing" {
					for _, tool := range strings.Split(chain, ",") {
						installParityTool(t, tools, tool)
					}
				}
				if variant == "hanging" || variant == "overflow" {
					runParityPreviewResourceSubprocess(t, path, tools, logPath, mode, strings.Split(chain, ",")[0])
					return
				}
				cache, err := preview.NewCache(filepath.Join(t.TempDir(), "cache"), 8<<20)
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				var mu sync.Mutex
				live, maxLive, starts := 0, 0, 0
				dispatches := []string{}
				options := preview.Options{
					Columns: 80, Lines: 24, Stdout: &output, Stderr: &output, Cache: cache,
					Environment: []string{"PATH=" + tools, "TERM=xterm-kitty", parityHelperEnvironment + "=" + mode, "PARITY_TEST_TOOL_LOG=" + logPath},
					Limits: preview.Limits{Deadline: 150 * time.Millisecond, MaxOutputBytes: 256, MaxInternalInputBytes: 1 << 20,
						MaxInternalLines: 100, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 1 << 20, MaxArtifactBytes: 4 << 20},
					Runner: processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
						mu.Lock()
						defer mu.Unlock()
						switch event.Phase {
						case "start":
							starts++
							live++
							if live > maxLive {
								maxLive = live
							}
						case "exit":
							live--
						}
					}},
					OnDispatch: func(name string, _ int, _ time.Duration) {
						mu.Lock()
						dispatches = append(dispatches, name)
						mu.Unlock()
					},
				}
				err = preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(path)}, options)
				mu.Lock()
				gotStarts, gotMaxLive, gotLive := starts, maxLive, live
				gotDispatches := append([]string(nil), dispatches...)
				mu.Unlock()
				if gotLive != 0 || gotMaxLive > golden.MaximumLiveChildren || gotStarts > golden.MaximumSequentialChildren {
					t.Fatalf("children starts/live/max=%d/%d/%d", gotStarts, gotLive, gotMaxLive)
				}
				logs := readParityToolLogs(t, logPath)
				switch variant {
				case "missing":
					if err != nil || output.Len() == 0 || gotStarts != 0 || !reflect.DeepEqual(gotDispatches, []string{"native"}) || len(logs) != 0 {
						t.Fatalf("missing err=%v output=%d starts=%d dispatch=%q logs=%+v", err, output.Len(), gotStarts, gotDispatches, logs)
					}
				case "present":
					if err != nil || output.Len() == 0 || gotStarts == 0 || len(gotDispatches) != 1 || gotDispatches[0] != strings.Split(chain, ",")[0] {
						t.Fatalf("present err=%v output=%d starts=%d dispatch=%q", err, output.Len(), gotStarts, gotDispatches)
					}
					assertParityToolReceivedPath(t, logs, path)
				case "failure":
					if err != nil || output.Len() == 0 || gotStarts == 0 || len(gotDispatches) != 1 || gotDispatches[0] != strings.Split(chain, ",")[0] {
						t.Fatalf("failure fallback err=%v output=%d starts=%d dispatch=%q", err, output.Len(), gotStarts, gotDispatches)
					}
					assertParityToolReceivedPath(t, logs, path)
				case "hanging":
					if !errors.Is(err, preview.ErrTerminalResource) || !errors.Is(err, context.DeadlineExceeded) || gotStarts != 1 ||
						len(gotDispatches) != 1 || gotDispatches[0] == "native" {
						t.Fatalf("hanging err=%v starts=%d dispatch=%q", err, gotStarts, gotDispatches)
					}
					assertParityProcessesGone(t, logs)
				case "overflow":
					if !errors.Is(err, preview.ErrTerminalResource) || !errors.Is(err, preview.ErrOutputLimit) || gotStarts != 1 ||
						len(gotDispatches) != 1 || gotDispatches[0] == "native" || output.Len() > int(options.Limits.MaxOutputBytes) {
						t.Fatalf("overflow err=%v output=%d starts=%d dispatch=%q", err, output.Len(), gotStarts, gotDispatches)
					}
				}
			})
		}
	}
}

func runParityPreviewResourceSubprocess(t *testing.T, fixture, tools, toolLog, mode, firstTool string) {
	t.Helper()
	root := t.TempDir()
	dispatchLog := filepath.Join(root, "dispatch")
	processLog := filepath.Join(root, "process")
	controlled := map[string]string{
		"PARITY_PREVIEW_RESOURCE_HELPER": "1", "PARITY_PREVIEW_FIXTURE": fixture, "PARITY_PREVIEW_TOOLS": tools,
		"PARITY_PREVIEW_TOOL_LOG": toolLog, "PARITY_PREVIEW_MODE": mode, "PARITY_PREVIEW_DISPATCH": dispatchLog,
		"PARITY_PREVIEW_PROCESS": processLog,
	}
	var output bytes.Buffer
	runner := processpkg.Runner{}
	err := runner.Run(context.Background(), processpkg.Spec{Path: paritySelfExecutable(t),
		Args: []string{"-test.run=^TestParityPreviewResourceProcess$", "-test.v"},
		Env:  processpkg.SanitizeEnv(os.Environ(), controlled), Stdout: &output, Stderr: &output,
		Containment: processpkg.ContainmentOwnTree, WaitDelay: time.Second})
	if err == nil {
		t.Fatalf("resource helper survived terminal preview: %s", output.String())
	}
	dispatch, readErr := os.ReadFile(dispatchLog)
	if readErr != nil || string(dispatch) != firstTool+"\n" {
		t.Fatalf("resource dispatch=%q err=%v helper=%v output=%s", dispatch, readErr, err, output.String())
	}
	processes, readErr := os.ReadFile(processLog)
	if readErr != nil || strings.Count(string(processes), "start\n") != 1 || strings.Contains(string(processes), "native") {
		t.Fatalf("resource process events=%q err=%v", processes, readErr)
	}
	logs := readParityToolLogs(t, toolLog)
	assertParityToolReceivedPath(t, logs, fixture)
	if mode == "tool-hang" {
		assertParityProcessesGone(t, logs)
	} else if len(logs) != 1 {
		t.Fatalf("overflow tool logs=%+v", logs)
	}
}

func TestParityPreviewResourceProcess(t *testing.T) {
	if os.Getenv("PARITY_PREVIEW_RESOURCE_HELPER") != "1" {
		return
	}
	appendMarker := func(path, marker string) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(marker + "\n"); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	options := preview.Options{
		Columns: 80, Lines: 24, Stdout: &output, Stderr: &output,
		Environment: []string{"PATH=" + os.Getenv("PARITY_PREVIEW_TOOLS"), "TERM=xterm-kitty",
			parityHelperEnvironment + "=" + os.Getenv("PARITY_PREVIEW_MODE"), "PARITY_TEST_TOOL_LOG=" + os.Getenv("PARITY_PREVIEW_TOOL_LOG")},
		Limits: preview.Limits{Deadline: 150 * time.Millisecond, MaxOutputBytes: 256, MaxInternalInputBytes: 1 << 20,
			MaxInternalLines: 100, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 1 << 20, MaxArtifactBytes: 4 << 20},
		Runner: processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
			if event.Phase == "start" || event.Phase == "exit" {
				appendMarker(os.Getenv("PARITY_PREVIEW_PROCESS"), event.Phase)
			}
		}},
		OnDispatch: func(name string, _ int, _ time.Duration) {
			appendMarker(os.Getenv("PARITY_PREVIEW_DISPATCH"), name)
		},
	}
	if os.Getenv("PARITY_PREVIEW_MODE") == "tool-overflow" {
		options.Limits.MaxOutputBytes = 64
	}
	_ = preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(os.Getenv("PARITY_PREVIEW_FIXTURE"))}, options)
	t.Fatal("terminal preview returned without killing inherited callback group")
}

func writeParityPreviewFixture(t *testing.T, category string) string {
	t.Helper()
	root := t.TempDir()
	if category == "directory" {
		path := filepath.Join(root, "directory --option with space")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(root, "--option with space."+map[string]string{
		"markdown": "md", "text": "txt", "image": "png", "pdf": "pdf", "video": "mp4", "audio": "mp3",
		"zip": "zip", "gzip": "gz", "xz": "xz", "tar": "tar", "bzip": "bz2", "binary": "bin",
	}[category])
	data := map[string][]byte{
		"markdown": []byte("# markdown\n"), "text": []byte("plain text\n"), "image": []byte("\x89PNG\r\n\x1a\n"),
		"pdf": []byte("%PDF-1.7\n"), "video": []byte("0000ftypvideo"), "audio": []byte("ID3audio"),
		"xz": []byte("\xfd7zXZ\x00payload"), "bzip": []byte("BZhpayload"), "binary": {0, 1, 2, 3},
	}
	switch category {
	case "zip":
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		entry, err := writer.Create("entry.txt")
		if err == nil {
			_, err = entry.Write([]byte("zip"))
		}
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	case "gzip":
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := gzip.NewWriter(file)
		_, err = writer.Write([]byte("gzip"))
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	case "tar":
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := tar.NewWriter(file)
		err = writer.WriteHeader(&tar.Header{Name: "entry.txt", Mode: 0o600, Size: 3})
		if err == nil {
			_, err = writer.Write([]byte("tar"))
		}
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	default:
		if err := os.WriteFile(path, data[category], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func installParityTool(t *testing.T, directory, name string) {
	t.Helper()
	copyNushellExecutable(t, paritySelfExecutable(t), filepath.Join(directory, name))
}

func readParityToolLogs(t *testing.T, path string) []parityToolLog {
	t.Helper()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var logs []parityToolLog
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var log parityToolLog
		if err := json.Unmarshal(scanner.Bytes(), &log); err != nil {
			t.Fatal(err)
		}
		logs = append(logs, log)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return logs
}

func assertParityToolReceivedPath(t *testing.T, logs []parityToolLog, path string) {
	t.Helper()
	for _, log := range logs {
		for _, argument := range log.Args[1:] {
			if argument == path {
				return
			}
		}
	}
	t.Fatalf("no tool received exact absolute path %q: %+v", path, logs)
}

func assertParityProcessesGone(t *testing.T, logs []parityToolLog) {
	t.Helper()
	if len(logs) < 2 {
		t.Fatalf("hanging renderer did not start a descendant: %+v", logs)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		allGone := true
		for _, log := range logs {
			if err := syscall.Kill(log.PID, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
				allGone = false
				break
			}
		}
		if allGone {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("renderer tree still alive: %+v", logs)
		case <-ticker.C:
		}
	}
}

func TestParityDeterministicSortFixtures(t *testing.T) {
	root := t.TempDir()
	names := []string{"A", "a", "Ä", "ä", string([]byte{'r', 'a', 'w', '-', 0xff, '-', 'A'}), string([]byte{'r', 'a', 'w', '-', 0xff, '-', 'a'})}
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	records, err := candidate.EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Filesystem([]byte(root)), candidate.LocalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := parityDisplays(records[2:])
	want := []string{"A", "a", `raw-\xFF-A`, `raw-\xFF-a`, "Ä", "ä"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deterministic byte order=%q, want %q", got, want)
	}
}

func TestParityZoxideCachedAndFreshContracts(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	request := candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte(root)), Initial: true}
	newCache := func(runner processpkg.Runner, path string) *candidate.ZoxideCache {
		cache, err := candidate.NewZoxideCache(runner, path, replaceEnvironment(os.Environ(), parityHelperEnvironment+"=zoxide-ok", "PARITY_TEST_ROOT="+root), 0)
		if err != nil {
			t.Fatal(err)
		}
		return cache
	}
	var cachedEventsMu sync.Mutex
	cachedEvents := []processpkg.ProcessEvent{}
	cachedRunner := processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
		cachedEventsMu.Lock()
		cachedEvents = append(cachedEvents, event)
		cachedEventsMu.Unlock()
	}}
	cached := candidate.Builder{}
	cached.ConfigureCached(newCache(cachedRunner, paritySelfExecutable(t)))
	first, err := cached.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Initial = false
	second, err := cached.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parityDisplays(first.Records), parityDisplays(second.Records)) || countProcessPhase(cachedEvents, "attempt") != 1 ||
		countProcessPhase(cachedEvents, "start") != 1 || second.Metrics.ZoxideOutcome != "cached" {
		t.Fatalf("cached first=%+v second=%+v events=%+v", first, second, cachedEvents)
	}

	missingEvents := []processpkg.ProcessEvent{}
	missingRunner := processpkg.Runner{Observe: func(event processpkg.ProcessEvent) { missingEvents = append(missingEvents, event) }}
	missing := candidate.Builder{}
	missing.ConfigureCached(newCache(missingRunner, filepath.Join(root, "missing-zoxide")))
	request.Initial = true
	missingResult, err := missing.Build(context.Background(), request)
	if err != nil || !missingResult.ZoxideDiscarded || countProcessPhase(missingEvents, "attempt") != 1 || countProcessPhase(missingEvents, "start") != 0 {
		t.Fatalf("missing result=%+v err=%v events=%+v", missingResult, err, missingEvents)
	}

	freshEvents := []processpkg.ProcessEvent{}
	freshRunner := processpkg.Runner{Observe: func(event processpkg.ProcessEvent) { freshEvents = append(freshEvents, event) }}
	fresh := candidate.Builder{}
	fresh.ConfigureFresh(func() (*candidate.ZoxideCache, error) { return newCache(freshRunner, paritySelfExecutable(t)), nil })
	for _, initial := range []bool{true, false} {
		request.Initial = initial
		result, err := fresh.Build(context.Background(), request)
		if err != nil || result.Metrics.ZoxideAttempts != 1 || result.Metrics.ZoxideStarts != 1 || result.Metrics.ZoxideMaxLive != 1 {
			t.Fatalf("fresh initial=%v result=%+v err=%v", initial, result, err)
		}
	}
	if countProcessPhase(freshEvents, "attempt") != 2 || countProcessPhase(freshEvents, "start") != 2 {
		t.Fatalf("fresh events=%+v", freshEvents)
	}
}

func countProcessPhase(events []processpkg.ProcessEvent, phase string) int {
	count := 0
	for _, event := range events {
		if event.Phase == phase {
			count++
		}
	}
	return count
}
