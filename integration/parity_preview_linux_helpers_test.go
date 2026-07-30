//go:build linux

package integration

import (
	"bytes"
	"context"
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
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

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
	localDisplays := []string{".", "..", "local"}
	mergedDisplays := []string{".", "..", "local", filepath.Join(root, "visible"), filepath.Join(root, "zoxide-one"), filepath.Join(root, "zoxide-two")}
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
	if !reflect.DeepEqual(parityDisplays(first.Records), mergedDisplays) || countRecordKind(first.Records, protocol.KindZoxide) != 3 ||
		first.Metrics.ZoxideAttempts != 1 || first.Metrics.ZoxideStarts != 1 {
		t.Fatalf("cached initial=%+v", first)
	}
	assertLocalOnlyNotRun(t, second, localDisplays)
	if countProcessPhase(cachedEvents, "attempt") != 1 || countProcessPhase(cachedEvents, "start") != 1 {
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
	request.Initial = true
	freshInitial, err := fresh.Build(context.Background(), request)
	if err != nil || !reflect.DeepEqual(parityDisplays(freshInitial.Records), mergedDisplays) || countRecordKind(freshInitial.Records, protocol.KindZoxide) != 3 ||
		freshInitial.Metrics.ZoxideAttempts != 1 || freshInitial.Metrics.ZoxideStarts != 1 {
		t.Fatalf("fresh initial=%+v err=%v", freshInitial, err)
	}
	request.Initial = false
	freshNavigation, err := fresh.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertLocalOnlyNotRun(t, freshNavigation, localDisplays)
	if countProcessPhase(freshEvents, "attempt") != 1 || countProcessPhase(freshEvents, "start") != 1 {
		t.Fatalf("fresh events=%+v", freshEvents)
	}
}

func assertLocalOnlyNotRun(t *testing.T, result candidate.BuildResult, wantDisplays []string) {
	t.Helper()
	metrics := result.Metrics
	if !reflect.DeepEqual(parityDisplays(result.Records), wantDisplays) || countRecordKind(result.Records, protocol.KindZoxide) != 0 || result.ZoxideDiscarded ||
		metrics.ZoxideOutcome != "not-run" || metrics.ZoxideAttempts != 0 || metrics.ZoxideStarts != 0 ||
		metrics.ZoxideExits != 0 || metrics.ZoxideProcesses != 0 || metrics.ZoxideLive != 0 || metrics.ZoxideMaxLive != 0 {
		t.Fatalf("local-only result=%+v", result)
	}
}

func countRecordKind(records []candidate.Record, kind protocol.Kind) int {
	count := 0
	for _, record := range records {
		if record.Kind == kind {
			count++
		}
	}
	return count
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
