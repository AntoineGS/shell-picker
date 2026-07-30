//go:build linux

package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestLinuxAuthorityDifferential(t *testing.T) {
	root := os.Getenv("SHELL_PICKER_AUTHORITY_ROOT")
	if root == "" {
		t.Skip("SHELL_PICKER_AUTHORITY_ROOT is not set")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := loadParityManifest(t)
	paths := map[string]string{
		"fzf-picker-candidates.zsh": filepath.Join(root, "Linux", "fzf", "fzf-picker-candidates.zsh"),
		"fzf-preview.sh":            filepath.Join(root, "Linux", "fzf", "fzf-preview.sh"),
		"fzf-batch-encode.pl":       filepath.Join(root, "Linux", "fzf", "fzf-batch-encode.pl"),
		"fzf-picker.test.zsh":       filepath.Join(root, "Linux", "fzf", "tests", "fzf-picker.test.zsh"),
		".zshrc":                    filepath.Join(root, "Linux", "zsh", ".zshrc"),
	}
	for name, path := range paths {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read authority %s: %v", name, readErr)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(raw))
		if got != manifest.Sources[name] {
			t.Fatalf("authority %s SHA-256=%s, want %s", name, got, manifest.Sources[name])
		}
	}

	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatal("zsh is required for the opted-in authority differential")
	}
	toolDirectory := t.TempDir()
	for _, tool := range []string{"base64", "bat", "fd", "file", "realpath"} {
		copyNushellExecutable(t, paritySelfExecutable(t), filepath.Join(toolDirectory, tool))
	}
	environment := replaceEnvironment(os.Environ(),
		"PATH="+toolDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
		parityHelperEnvironment+"=authority-tool", "LC_ALL=C.UTF-8", "LANG=C.UTF-8")

	counts := make(map[string]int)
	for _, row := range loadParityMatrix(t) {
		counts[row.Suite]++
	}
	greenSuites := []string{
		"codec", "batch-encoder", "directory-enumeration", "cd-merged", "operations", "slash", "modal",
		"create", "preview", "zshrc-cd", "zshrc-cp", "zshrc-add-mode-navigation-bindings",
	}
	total := 0
	for _, suite := range greenSuites {
		t.Run("legacy-"+suite, func(t *testing.T) {
			command := exec.Command(zsh, paths["fzf-picker.test.zsh"], suite)
			command.Dir = root
			command.Env = environment
			output, runErr := command.CombinedOutput()
			want := fmt.Sprintf("PASS: %d assertions\n", counts[suite])
			if runErr != nil || string(output) != want {
				t.Fatalf("legacy suite %s: %v\noutput=%q\nwant=%q", suite, runErr, output, want)
			}
		})
		total += counts[suite]
	}
	if total != 365 {
		t.Fatalf("green legacy assertion total=%d, want 365", total)
	}

	t.Run("known-query-binding-diagnostic", func(t *testing.T) {
		const wantDiagnosticSHA256 = "453e680896524eb21763e1befdae9324da2d333974e0534d7fe15db8ae4893cb"
		counter := filepath.Join(t.TempDir(), "assertions")
		script := "TRAPDEBUG() { [[ $ZSH_DEBUG_CMD == assert_* && $ZSH_DEBUG_CMD != *' () {'* ]] && print x >> $AUTHORITY_ASSERTION_COUNTER }\n" +
			"source \"$1\" zshrc-add-mode-query-bindings"
		command := exec.Command(zsh, "-fc", script, "authority-query", paths["fzf-picker.test.zsh"])
		command.Dir = root
		command.Env = replaceEnvironment(environment, "AUTHORITY_ASSERTION_COUNTER="+counter)
		output, runErr := command.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("known query suite error=%v output=%q", runErr, output)
		}
		calls, readErr := os.ReadFile(counter)
		if readErr != nil || string(calls) != "x\nx\nx\nx\n" {
			t.Fatalf("known query suite assertion calls=%q err=%v", calls, readErr)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(output))
		if got != wantDiagnosticSHA256 {
			t.Fatalf("known indentation diagnostic SHA-256=%s, want %s", got, wantDiagnosticSHA256)
		}
	})

	replacements := 0
	for _, row := range loadParityMatrix(t) {
		if row.Suite != "zshrc-add-mode-query-bindings" {
			continue
		}
		row := row
		t.Run("replacement-"+row.ID, func(t *testing.T) { runParityRow(t, row) })
		replacements++
	}
	if replacements != 6 {
		t.Fatalf("semantic replacements=%d, want 6", replacements)
	}
	assertAuthorityFreshLaunchZoxide(t)
}

func assertAuthorityFreshLaunchZoxide(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	events := []processpkg.ProcessEvent{}
	runner := processpkg.Runner{Observe: func(event processpkg.ProcessEvent) { events = append(events, event) }}
	builder := candidate.Builder{}
	builder.ConfigureFresh(func() (*candidate.ZoxideCache, error) {
		return candidate.NewZoxideCache(runner, paritySelfExecutable(t),
			replaceEnvironment(os.Environ(), parityHelperEnvironment+"=zoxide-ok", "PARITY_TEST_ROOT="+root), 0)
	})
	request := candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte(root)), Initial: true}
	initial, err := builder.Build(context.Background(), request)
	mergedDisplays := []string{".", "..", "local", filepath.Join(root, "visible"), filepath.Join(root, "zoxide-one"), filepath.Join(root, "zoxide-two")}
	if err != nil || !reflect.DeepEqual(parityDisplays(initial.Records), mergedDisplays) || countRecordKind(initial.Records, protocol.KindZoxide) != 3 ||
		initial.Metrics.ZoxideAttempts != 1 || initial.Metrics.ZoxideStarts != 1 || initial.Metrics.ZoxideMaxLive != 1 {
		t.Fatalf("fresh authority initial=%+v err=%v", initial, err)
	}
	request.Initial = false
	navigation, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertLocalOnlyNotRun(t, navigation, []string{".", "..", "local"})
	if countProcessPhase(events, "attempt") != 1 || countProcessPhase(events, "start") != 1 {
		t.Fatalf("fresh authority events=%+v", events)
	}
}
