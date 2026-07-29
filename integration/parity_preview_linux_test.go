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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func installParityTool(t *testing.T, directory, name string) {
	t.Helper()
	// Do not copy the race-instrumented Go test binary: its startup alone can
	// exceed the production preview deadline.  This deterministic POSIX tool
	// records NUL-framed argv and implements the renderer contract directly.
	body := `#!/bin/sh
name=${0##*/}
printf '%s\0' "$PARITY_TEST_HELPER" "$$" "$name" "$@" '' >> "$PARITY_TEST_TOOL_LOG"
case "$PARITY_TEST_HELPER" in
  tool-fail) exit 7 ;;
  tool-hang)
    printf '%s\n' tool-hang > "$PARITY_TEST_TERMINAL"
    /bin/sleep 10 & child=$!
    printf '%s\0' tool-descendant "$child" sleep '' >> "$PARITY_TEST_TOOL_LOG"
    wait
    ;;
  tool-overflow)
    /bin/sleep 0.11
    payload=01234567890123456789012345678901234567890123456789012345678901234567890123456789
    printf 'tool-overflow:emitted=%s\n' "${#payload}" > "$PARITY_TEST_TERMINAL"
    case "$name" in file) printf '%s' "$payload" >&2 ;; *) printf '%s' "$payload" ;; esac
    /bin/sleep 10
    ;;
  tool-slow)
    printf '%s\n' tool-slow > "$PARITY_TEST_TERMINAL"
    /bin/sleep 0.3
    ;;
esac
case "$name" in
  pdftoppm) printf 'parity-artifact\n' > "$4.jpg" ;;
  ffmpegthumbnailer)
    output=
    next=
    for argument in "$@"; do
      case "$next" in yes) output=$argument; break ;; esac
      case "$argument" in -o) next=yes ;; esac
    done
    case "$output" in '') exit 123 ;; *) printf 'parity-artifact\n' > "$output" ;; esac
    ;;
  ffmpeg)
    output=
    for argument in "$@"; do output=$argument; done
    case "$output" in '') exit 123 ;; *) printf 'parity-artifact\n' > "$output" ;; esac
    ;;
  file) printf 'application/octet-stream\n' ;;
  *) printf 'parity-%s-preview\n' "$PARITY_TEST_CATEGORY" ;;
esac
`
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestParityToolLogParserRejectsMutations(t *testing.T) {
	valid := []byte("tool-success\x00123\x00glow\x00--width\x0079\x00/absolute/fixture.md\x00\x00")
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "truncated-last-terminator", raw: valid[:len(valid)-1]},
		{name: "malformed-later-record", raw: append(append([]byte(nil), valid...),
			[]byte("tool-success\x00not-a-pid\x00bat\x00--\x00/absolute/fixture.md\x00\x00")...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if logs, err := parseParityToolLogs(test.raw); err == nil {
				t.Fatalf("mutation parsed as %+v", logs)
			}
		})
	}
}

func readParityToolLogs(t *testing.T, path string) []parityToolLog {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	logs, err := parseParityToolLogs(raw)
	if err != nil {
		t.Fatal(err)
	}
	return logs
}

func parseParityToolLogs(raw []byte) ([]parityToolLog, error) {
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return nil, errors.New("tool log is empty or does not end in NUL")
	}
	frames := bytes.Split(raw, []byte{0})
	last := len(frames) - 1
	logs := make([]parityToolLog, 0)
	for index := 0; index < last; {
		if index+3 >= last {
			return nil, fmt.Errorf("tool log record %d is incomplete", len(logs)+1)
		}
		mode, pidText, name := string(frames[index]), string(frames[index+1]), string(frames[index+2])
		if mode == "" || name == "" {
			return nil, fmt.Errorf("tool log record %d has an empty mode or tool name", len(logs)+1)
		}
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 || strconv.Itoa(pid) != pidText {
			return nil, fmt.Errorf("tool log record %d has invalid PID %q", len(logs)+1, pidText)
		}
		index += 3
		var args []string
		for index < last && len(frames[index]) != 0 {
			args = append(args, string(frames[index]))
			index++
		}
		if index >= last {
			return nil, fmt.Errorf("tool log record %d has no empty-frame terminator", len(logs)+1)
		}
		index++
		logs = append(logs, parityToolLog{Mode: mode, PID: pid, Name: name, Args: args})
	}
	return logs, nil
}

func assertParityToolEvidence(t *testing.T, category, mode, firstTool string, logs []parityToolLog, fixture string,
	processes []previewProcessRecord, terminal bool) {
	t.Helper()
	var primary, descendants []parityToolLog
	for _, log := range logs {
		if log.Mode == "tool-descendant" {
			descendants = append(descendants, log)
			continue
		}
		if log.Mode != mode {
			t.Fatalf("tool mode=%q, want %q: %+v", log.Mode, mode, logs)
		}
		primary = append(primary, log)
	}
	wantNames := expectedParityToolNames(category, mode, firstTool)
	gotNames := make([]string, len(primary))
	for index, log := range primary {
		gotNames[index] = log.Name
		assertParityToolArguments(t, category, mode, log, fixture, index > 0)
	}
	if !reflect.DeepEqual(gotNames, wantNames) || len(primary) == 0 || primary[0].Name != firstTool {
		t.Fatalf("%s/%s primary tools=%q, want %q (first %q)", category, mode, gotNames, wantNames, firstTool)
	}
	if mode == "tool-hang" {
		if len(descendants) != 1 || descendants[0].Name != "sleep" || len(descendants[0].Args) != 0 || descendants[0].PID == primary[0].PID {
			t.Fatalf("hanging descendant log=%+v primary=%+v", descendants, primary)
		}
	} else if len(descendants) != 0 {
		t.Fatalf("unexpected descendant logs=%+v", descendants)
	}
	var starts, exits []int
	for _, process := range processes {
		switch process.Phase {
		case "start":
			starts = append(starts, process.PID)
		case "exit":
			exits = append(exits, process.PID)
		}
	}
	primaryPIDs := make([]int, len(primary))
	for index, log := range primary {
		primaryPIDs[index] = log.PID
	}
	if !reflect.DeepEqual(primaryPIDs, starts) {
		t.Fatalf("primary tool PIDs=%v, process starts=%v logs=%+v processes=%+v", primaryPIDs, starts, primary, processes)
	}
	if terminal {
		if len(starts) != 1 || len(exits) != 0 {
			t.Fatalf("terminal process sequence starts/exits=%v/%v", starts, exits)
		}
	} else if !reflect.DeepEqual(exits, starts) {
		t.Fatalf("process exits=%v, want starts=%v", exits, starts)
	}
}

func expectedParityToolNames(category, mode, firstTool string) []string {
	if mode == "tool-hang" || mode == "tool-overflow" || mode == "tool-slow" {
		return []string{firstTool}
	}
	present := map[string][]string{
		"directory": {"eza"}, "markdown": {"glow"}, "text": {"bat"}, "image": {"kitten"},
		"pdf": {"pdftoppm", "kitten"}, "video": {"ffmpegthumbnailer", "kitten"}, "audio": {"ffmpeg", "kitten"},
		"zip": {"unzip"}, "gzip": {"gzip"}, "xz": {"xz"}, "tar": {"tar"}, "bzip": {"tar"}, "binary": {"file"},
	}
	failure := map[string][]string{
		"directory": {"eza"}, "markdown": {"glow"}, "text": {"bat"}, "image": {"kitten", "chafa"},
		"pdf": {"pdftoppm"}, "video": {"ffmpegthumbnailer"}, "audio": {"ffmpeg", "exiftool"},
		"zip": {"unzip"}, "gzip": {"gzip"}, "xz": {"xz"}, "tar": {"tar"}, "bzip": {"tar"}, "binary": {"file"},
	}
	if mode == "tool-success" {
		return present[category]
	}
	if mode == "tool-fail" {
		return failure[category]
	}
	return nil
}

func assertParityToolArguments(t *testing.T, category, mode string, log parityToolLog, fixture string, later bool) {
	t.Helper()
	if !filepath.IsAbs(fixture) {
		t.Fatalf("fixture is not absolute: %q", fixture)
	}
	if category != "directory" && !strings.HasPrefix(filepath.Base(fixture), "--option") {
		t.Fatalf("fixture does not retain leading-option basename: %q", fixture)
	}
	source := fixture
	if category == "pdf" || category == "video" || category == "audio" {
		if log.Name == "kitten" || log.Name == "chafa" {
			if mode != "tool-success" || !later {
				t.Fatalf("%s renderer lacks successful converter predecessor: %+v", category, log)
			}
			source = "/proc/self/fd/3"
		}
	}
	want := map[string][]string{
		"eza":      {"--color=always", "--icons=always", "--group-directories-first", "--", fixture},
		"glow":     {"--width", "79", fixture},
		"bat":      {"--color=always", "--style=plain", "--paging=never", "--", fixture},
		"kitten":   {"icat", "--clear", "--transfer-mode=memory", "--place", "80x24@0x0", "--", source},
		"chafa":    {"--size", "80x24", "--", source},
		"exiftool": {"--", fixture},
		"unzip":    {"-l", "--", fixture},
		"gzip":     {"-l", "--", fixture},
		"xz":       {"-l", "--", fixture},
		"tar":      {"tf", fixture},
		"file":     {"--brief", "--mime-type", "--", fixture},
	}[log.Name]
	switch log.Name {
	case "pdftoppm":
		if len(log.Args) != 4 || !filepath.IsAbs(log.Args[3]) || log.Args[3] == fixture {
			t.Fatalf("pdftoppm artifact argv=%q", log.Args)
		}
		want = []string{"-singlefile", "-jpeg", fixture, log.Args[3]}
	case "ffmpegthumbnailer":
		if len(log.Args) != 7 || !filepath.IsAbs(log.Args[3]) || filepath.Ext(log.Args[3]) != ".jpg" {
			t.Fatalf("ffmpegthumbnailer artifact argv=%q", log.Args)
		}
		want = []string{"-i", fixture, "-o", log.Args[3], "-s", "1080", "-m"}
	case "ffmpeg":
		if len(log.Args) != 7 || !filepath.IsAbs(log.Args[6]) || filepath.Ext(log.Args[6]) != ".jpg" {
			t.Fatalf("ffmpeg artifact argv=%q", log.Args)
		}
		want = []string{"-y", "-i", fixture, "-an", "-c:v", "copy", log.Args[6]}
	}
	if want == nil {
		t.Fatalf("unknown parity preview tool %q", log.Name)
	}
	if !reflect.DeepEqual(log.Args, want) {
		t.Fatalf("%s/%s argv=%q, want %q", category, log.Name, log.Args, want)
	}
	for _, argument := range log.Args {
		if argument == fixture || argument == source && source != fixture || strings.HasPrefix(argument, "/proc/") {
			if !filepath.IsAbs(argument) {
				t.Fatalf("%s path operand is not absolute: %q", log.Name, argument)
			}
		}
	}
}

func assertParityCategoryPreview(t *testing.T, category, output string, external bool) {
	t.Helper()
	if external && category != "binary" {
		want := "parity-" + category + "-preview\n"
		if output != want {
			t.Fatalf("%s external preview=%q, want %q", category, output, want)
		}
		return
	}
	want := map[string][]string{
		"directory": {"Directory:", "directory-entry.txt"},
		"markdown":  {"# markdown"},
		"text":      {"plain text"},
		"image":     {"image file:"},
		"pdf":       {"PDF document:", "%PDF-1.7"},
		"video":     {"video file:"},
		"audio":     {"audio file:"},
		"zip":       {"entry.txt"},
		"gzip":      {"Gzip archive:"},
		"xz":        {"xz file:"},
		"tar":       {"entry.txt"},
		"bzip":      {"bzip file:"},
		"binary":    {"binary file:"},
	}[category]
	for _, marker := range want {
		if !strings.Contains(output, marker) {
			t.Fatalf("%s native preview %q lacks fixture marker %q", category, output, marker)
		}
	}
}

func assertParityProcessesGone(t *testing.T, logs []parityToolLog) {
	t.Helper()
	if len(logs) == 0 || logs[0].Mode == "tool-hang" && len(logs) < 2 {
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
