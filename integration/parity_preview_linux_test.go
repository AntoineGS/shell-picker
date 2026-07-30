//go:build linux

package integration

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	Name string   `json:"name"`
	Args []string `json:"args"`
}

type previewProcessRecord struct {
	Phase string `json:"phase"`
	PID   int    `json:"pid"`
}

type previewResourceOutcome struct {
	Error       string                 `json:"error"`
	Output      string                 `json:"output"`
	Dispatch    []string               `json:"dispatch"`
	Processes   []previewProcessRecord `json:"processes"`
	RenderNanos int64                  `json:"render_nanos"`
}

type previewProcessStats struct {
	Starts, Exits, MaxLive int
}

const parityOverflowPayload = "01234567890123456789012345678901234567890123456789012345678901234567890123456789"

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
				// A renderer can still be starting when the preview deadline expires.
				// Only missing is guaranteed to start no tool, so every tool-capable
				// matrix case owns an isolated helper process group.
				if variant != "missing" {
					runParityPreviewResourceSubprocess(t, category, path, tools, logPath, mode, strings.Split(chain, ",")[0], variant)
					return
				}
				cache, err := preview.NewCache(filepath.Join(t.TempDir(), "cache"), 8<<20)
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				var mu sync.Mutex
				processes := []previewProcessRecord{}
				dispatches := []string{}
				options := preview.Options{
					Columns: 80, Lines: 24, Stdout: &output, Stderr: &output, Cache: cache,
					Environment: []string{"PATH=" + tools, "TERM=xterm-kitty", parityHelperEnvironment + "=" + mode, "PARITY_TEST_TOOL_LOG=" + logPath},
					Limits: preview.Limits{Deadline: 150 * time.Millisecond, MaxOutputBytes: 256, MaxInternalInputBytes: 1 << 20,
						MaxInternalLines: 100, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 1 << 20, MaxArtifactBytes: 4 << 20},
					Runner: processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
						mu.Lock()
						defer mu.Unlock()
						if event.Phase == "start" || event.Phase == "exit" {
							processes = append(processes, previewProcessRecord{Phase: event.Phase, PID: event.PID})
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
				gotProcesses := append([]previewProcessRecord(nil), processes...)
				gotDispatches := append([]string(nil), dispatches...)
				mu.Unlock()
				stats := assertPreviewProcessJournal(t, gotProcesses, false, golden)
				logs := readParityToolLogs(t, logPath)
				switch variant {
				case "missing":
					if err != nil || stats.Starts != 0 || stats.MaxLive != 0 || !reflect.DeepEqual(gotDispatches, []string{"native"}) || len(logs) != 0 {
						t.Fatalf("missing err=%v output=%q stats=%+v dispatch=%q logs=%+v", err, output.String(), stats, gotDispatches, logs)
					}
				}
				assertParityCategoryPreview(t, category, output.String(), false)
			})
		}
	}
}

func TestPreviewSlowStartIsolatedFromRaceParent(t *testing.T) {
	path := writeParityPreviewFixture(t, "markdown")
	tools, logPath := t.TempDir(), filepath.Join(t.TempDir(), "tools.jsonl")
	installParityTool(t, tools, "glow")
	started := time.Now()
	runParityPreviewResourceSubprocess(t, "markdown", path, tools, logPath, "tool-slow", "glow", "timeout")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("slow renderer escaped helper bound: %s", elapsed)
	}
}

func runParityPreviewResourceSubprocess(t *testing.T, category, fixture, tools, toolLog, mode, firstTool, variant string) {
	t.Helper()
	root := t.TempDir()
	dispatchLog := filepath.Join(root, "dispatch")
	rendererLog := filepath.Join(root, "renderer-process")
	helperLog := filepath.Join(root, "helper-process")
	terminalLog := filepath.Join(root, "terminal")
	capturePath := filepath.Join(root, "capture")
	outcomePath := filepath.Join(root, "outcome.json")
	controlled := map[string]string{
		"PARITY_PREVIEW_RESOURCE_HELPER": "1", "PARITY_PREVIEW_FIXTURE": fixture, "PARITY_PREVIEW_TOOLS": tools,
		"PARITY_PREVIEW_TOOL_LOG": toolLog, "PARITY_PREVIEW_MODE": mode, "PARITY_PREVIEW_DISPATCH": dispatchLog,
		"PARITY_PREVIEW_PROCESS": rendererLog, "PARITY_PREVIEW_OUTCOME": outcomePath, "PARITY_PREVIEW_CATEGORY": category,
		"PARITY_PREVIEW_TERMINAL": terminalLog, "PARITY_PREVIEW_CAPTURE": capturePath,
	}
	var output bytes.Buffer
	runner := processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
		if event.Phase == "start" || event.Phase == "exit" {
			appendPreviewProcessRecord(t, helperLog, event.Phase, event.PID)
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	err := runner.Run(ctx, processpkg.Spec{Path: paritySelfExecutable(t),
		Args: []string{"-test.run=^TestParityPreviewResourceProcess$", "-test.v"},
		Env:  processpkg.SanitizeEnv(os.Environ(), controlled), Stdout: &output, Stderr: &output,
		Containment: processpkg.ContainmentOwnTree, WaitDelay: 100 * time.Millisecond})
	elapsed := time.Since(started)
	helperStats := assertPreviewProcessJournal(t, readPreviewProcessJournal(t, helperLog), true, previewGolden{
		MaximumLiveChildren: 1, MaximumSequentialChildren: 1,
	})
	if helperStats.Starts != 1 || helperStats.Exits != 1 || helperStats.MaxLive != 1 {
		t.Fatalf("helper process stats=%+v", helperStats)
	}
	dispatch, readErr := os.ReadFile(dispatchLog)
	if readErr != nil || string(dispatch) != firstTool+"\n" {
		t.Fatalf("resource dispatch=%q err=%v helper=%v output=%s", dispatch, readErr, err, output.String())
	}
	logs := readParityToolLogs(t, toolLog)
	terminal := variant == "hanging" || variant == "overflow" || variant == "timeout"
	if terminal {
		var exitErr *processpkg.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 137 || errors.Is(err, context.DeadlineExceeded) ||
			elapsed < 100*time.Millisecond || elapsed > 500*time.Millisecond {
			t.Fatalf("%s helper err=%v elapsed=%s output=%s", variant, err, elapsed, output.String())
		}
		if raw, outcomeErr := os.ReadFile(outcomePath); !errors.Is(outcomeErr, os.ErrNotExist) {
			t.Fatalf("%s unexpectedly wrote final outcome %q err=%v", variant, raw, outcomeErr)
		}
		marker := strings.TrimSpace(string(mustReadParityFile(t, terminalLog)))
		wantMarker := map[string]string{"hanging": "tool-hang", "overflow": "tool-overflow:emitted=80", "timeout": "tool-slow"}[variant]
		if marker != wantMarker {
			t.Fatalf("%s terminal marker=%q, want %q", variant, marker, wantMarker)
		}
		rendererRecords := readPreviewProcessJournal(t, rendererLog)
		assertParityToolEvidence(t, category, mode, firstTool, logs, fixture, rendererRecords, true)
		captured := mustReadParityFile(t, capturePath)
		switch variant {
		case "hanging", "timeout":
			if len(captured) != 0 {
				t.Fatalf("%s captured renderer/native/diagnostic output %q", variant, captured)
			}
		case "overflow":
			want := []byte(parityOverflowPayload[:64])
			if !bytes.Equal(captured, want) {
				t.Fatalf("overflow captured=%q (%d bytes), want exact payload prefix %q", captured, len(captured), want)
			}
		}
		assertParityProcessesGone(t, logs)
		return
	}
	if err != nil {
		t.Fatalf("%s helper=%v: %s", variant, err, output.String())
	}
	var outcome previewResourceOutcome
	rawOutcome, readErr := os.ReadFile(outcomePath)
	if readErr != nil || json.Unmarshal(rawOutcome, &outcome) != nil {
		t.Fatalf("%s outcome=%q err=%v", variant, rawOutcome, readErr)
	}
	stats := assertPreviewProcessJournal(t, outcome.Processes, true, previewGolden{MaximumLiveChildren: 1, MaximumSequentialChildren: 3})
	if outcome.Error != "" || stats.Starts == 0 || stats.MaxLive != 1 || len(outcome.Dispatch) != 1 || outcome.Dispatch[0] != firstTool {
		t.Fatalf("%s outcome=%+v stats=%+v", variant, outcome, stats)
	}
	assertParityToolEvidence(t, category, mode, firstTool, logs, fixture, outcome.Processes, false)
	if variant == "present" && time.Duration(outcome.RenderNanos) >= 125*time.Millisecond {
		t.Fatalf("present render clustered at deadline: %s", time.Duration(outcome.RenderNanos))
	}
	assertParityCategoryPreview(t, category, outcome.Output, variant == "present")
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
	output, err := os.OpenFile(os.Getenv("PARITY_PREVIEW_CAPTURE"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	options := preview.Options{
		Columns: 80, Lines: 24, Stdout: output, Stderr: output,
		Environment: []string{"PATH=" + os.Getenv("PARITY_PREVIEW_TOOLS"), "TERM=xterm-kitty",
			parityHelperEnvironment + "=" + os.Getenv("PARITY_PREVIEW_MODE"), "PARITY_TEST_TOOL_LOG=" + os.Getenv("PARITY_PREVIEW_TOOL_LOG"),
			"PARITY_TEST_CATEGORY=" + os.Getenv("PARITY_PREVIEW_CATEGORY"), "PARITY_TEST_TERMINAL=" + os.Getenv("PARITY_PREVIEW_TERMINAL")},
		Limits: preview.Limits{Deadline: 150 * time.Millisecond, MaxOutputBytes: 256, MaxInternalInputBytes: 1 << 20,
			MaxInternalLines: 100, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 1 << 20, MaxArtifactBytes: 4 << 20},
		Runner: processpkg.Runner{Observe: func(event processpkg.ProcessEvent) {
			if event.Phase == "start" || event.Phase == "exit" {
				appendPreviewProcessRecord(t, os.Getenv("PARITY_PREVIEW_PROCESS"), event.Phase, event.PID)
			}
		}},
		OnDispatch: func(name string, _ int, _ time.Duration) {
			appendMarker(os.Getenv("PARITY_PREVIEW_DISPATCH"), name)
		},
	}
	if os.Getenv("PARITY_PREVIEW_MODE") == "tool-overflow" {
		options.Limits.MaxOutputBytes = 64
	}
	started := time.Now()
	err = preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(os.Getenv("PARITY_PREVIEW_FIXTURE"))}, options)
	renderElapsed := time.Since(started)
	if syncErr := output.Sync(); syncErr != nil {
		t.Fatal(syncErr)
	}
	rawOutput, readErr := os.ReadFile(os.Getenv("PARITY_PREVIEW_CAPTURE"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	dispatch := strings.Fields(string(mustReadParityFile(t, os.Getenv("PARITY_PREVIEW_DISPATCH"))))
	outcome, marshalErr := json.Marshal(previewResourceOutcome{Error: errorText(err), Output: string(rawOutput), Dispatch: dispatch,
		Processes: readPreviewProcessJournal(t, os.Getenv("PARITY_PREVIEW_PROCESS")), RenderNanos: renderElapsed.Nanoseconds()})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if writeErr := os.WriteFile(os.Getenv("PARITY_PREVIEW_OUTCOME"), outcome, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
}

func appendPreviewProcessRecord(t *testing.T, path, phase string, pid int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(phase + " " + strconv.Itoa(pid) + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readPreviewProcessJournal(t *testing.T, path string) []previewProcessRecord {
	t.Helper()
	raw := mustReadParityFile(t, path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	records := make([]previewProcessRecord, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid process journal line %q", line)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 || fields[0] != "start" && fields[0] != "exit" {
			t.Fatalf("invalid process journal line %q", line)
		}
		records = append(records, previewProcessRecord{Phase: fields[0], PID: pid})
	}
	return records
}

func assertPreviewProcessJournal(t *testing.T, records []previewProcessRecord, requireStart bool, golden previewGolden) previewProcessStats {
	t.Helper()
	live := make(map[int]bool)
	seen := make(map[int]bool)
	stats := previewProcessStats{}
	for _, record := range records {
		switch record.Phase {
		case "start":
			if seen[record.PID] {
				t.Fatalf("duplicate process start PID %d in %+v", record.PID, records)
			}
			seen[record.PID] = true
			live[record.PID] = true
			stats.Starts++
			stats.MaxLive = max(stats.MaxLive, len(live))
			if len(live) > golden.MaximumLiveChildren {
				t.Fatalf("process live=%d exceeds %d: %+v", len(live), golden.MaximumLiveChildren, records)
			}
		case "exit":
			if !live[record.PID] {
				t.Fatalf("process exit without live PID %d in %+v", record.PID, records)
			}
			delete(live, record.PID)
			stats.Exits++
		}
	}
	if requireStart && stats.Starts == 0 {
		t.Fatalf("process journal has zero starts: %+v", records)
	}
	if stats.Starts > golden.MaximumSequentialChildren {
		t.Fatalf("process starts=%d exceeds %d: %+v", stats.Starts, golden.MaximumSequentialChildren, records)
	}
	if len(live) != 0 {
		t.Fatalf("process journal has terminal survivors %v: %+v", live, records)
	}
	return stats
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func mustReadParityFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeParityPreviewFixture(t *testing.T, category string) string {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if category == "directory" {
		path := filepath.Join(root, "directory --option with space")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "directory-entry.txt"), []byte("entry\n"), 0o600); err != nil {
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
