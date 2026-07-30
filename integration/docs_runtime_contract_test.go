package integration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestDocumentedModeTableMatchesFZFBindings(t *testing.T) {
	want := map[string][]string{}
	for _, mode := range []protocol.Mode{protocol.ModeInsert, protocol.ModeNormal, protocol.ModeAdd} {
		want[modeTitle(mode)] = []string{
			modeTitle(mode),
			formatBindings(activeModeBindings(t, protocol.PickerCD, mode)),
			formatBindings(activeModeBindings(t, protocol.PickerCP, mode)),
		}
	}
	got := parseModeTable(t, readDoc(t, "README.md"))
	if strings.Join(flattenModeRows(got), "\n") != strings.Join(flattenModeRows(want), "\n") {
		t.Fatalf("README mode table differs from fzf bindings\n got: %#v\nwant: %#v", got, want)
	}
}

func TestArchitectureDocumentsActualOwnership(t *testing.T) {
	document := readDoc(t, "docs/architecture.md")
	required := []string{
		"Ready --> Pending: ProposedTransition",
		"`Handle` resolves each `AddIntent` before calling `Actor.Apply`; only the resulting `ProposedTransition` can become pending.",
		"Ignored events are ordinary `ProposedTransition` values carrying `Effect.Ignore`.",
		"Every preview child uses `ContainmentInheritTree` under its callback tree.",
		"Zoxide uses `ContainmentOwnTree`.",
		"Fzf uses `ContainmentForegroundTree`.",
	}
	for _, value := range required {
		if !strings.Contains(document, value) {
			t.Errorf("architecture missing runtime ownership statement %q", value)
		}
	}
	for _, stale := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)Pending[^\n]*(?:XOR|or)[^\n]*AddIntent|AddIntent\s*(?:->|\x{2192}|enters|reaches)\s*Pending`),
		regexp.MustCompile(`(?i)preview[^\n]*(?:own|either own)[^\n]*(?:tree|callback)`),
		regexp.MustCompile(`(?i)ignored event[^\n]*(?:neither|no side effect)`),
	} {
		if match := stale.FindString(document); match != "" {
			t.Errorf("architecture contains stale statement %q", match)
		}
	}
}

func TestDocumentationStatesLaunchOnlyZoxideContract(t *testing.T) {
	currentDocuments := []string{
		"README.md",
		"docs/architecture.md",
		"docs/adapters.md",
		"docs/performance.md",
		"docs/security.md",
		"docs/parity.md",
	}
	documentation := strings.Builder{}
	for _, path := range currentDocuments {
		document := readDoc(t, path)
		documentation.WriteString(document)
		documentation.WriteByte('\n')
		for _, stale := range []string{
			"immutable cached later navigation",
			"one attempt per completed fresh generation",
			"authoritative fresh navigation",
			"cached-navigation",
			"fresh-navigation",
			"fresh-exact-parity-navigation",
		} {
			if strings.Contains(document, stale) {
				t.Errorf("%s contains stale zoxide navigation claim %q", path, stale)
			}
		}
	}
	for _, required := range []string{
		"initial CD view",
		"navigation is local-only",
		"zoxide_outcome `not-run`",
		"navigation-local-only",
	} {
		if !strings.Contains(documentation.String(), required) {
			t.Errorf("current documentation missing launch-only zoxide concept %q", required)
		}
	}
}

func TestDocumentationStatesCurrentRuntimeContracts(t *testing.T) {
	documentation := readDoc(t, "README.md") + readDoc(t, "docs/protocol.md") +
		readDoc(t, "docs/architecture.md") + readDoc(t, "docs/security.md")
	for _, required := range []string{"i:cd", "i:cp", "change-header", "two-line layout"} {
		if !strings.Contains(documentation, required) {
			t.Errorf("current documentation missing runtime contract %q", required)
		}
	}
}

func activeModeBindings(t *testing.T, picker protocol.Picker, mode protocol.Mode) []string {
	t.Helper()
	options := fzf.Options(picker, "[I] ", "/work/")
	type binding struct {
		keys   []string
		render string
	}
	bindings := []binding{}
	active := map[string]bool{}
	for _, option := range options {
		if !strings.HasPrefix(option, "--bind=") {
			continue
		}
		render := strings.TrimPrefix(option, "--bind=")
		keys, _, ok := strings.Cut(render, ":")
		if !ok || keys == "start" || keys == "resize" {
			continue
		}
		parts := strings.Split(keys, ",")
		bindings = append(bindings, binding{keys: parts, render: render})
		for _, key := range parts {
			active[key] = true
		}
	}
	effect, err := fzf.RenderEffect(protocol.Effect{Rebind: mode})
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`(rebind|unbind)\(([^)]*)\)`).FindAllStringSubmatch(effect, -1) {
		for _, key := range strings.Split(match[2], ",") {
			active[key] = match[1] == "rebind"
		}
	}
	result := []string{}
	for _, binding := range bindings {
		allActive := true
		for _, key := range binding.keys {
			allActive = allActive && active[key]
		}
		if allActive {
			result = append(result, binding.render)
		}
	}
	return result
}

func parseModeTable(t *testing.T, document string) map[string][]string {
	t.Helper()
	lines := strings.Split(document, "\n")
	for index, line := range lines {
		if line != "| Mode | cd active fzf bindings | cp active fzf bindings |" {
			continue
		}
		if index+4 >= len(lines) || lines[index+1] != "| --- | --- | --- |" {
			t.Fatal("README mode table has invalid header")
		}
		rows := map[string][]string{}
		for _, line := range lines[index+2 : index+5] {
			cells := strings.Split(strings.Trim(line, "|"), "|")
			if len(cells) != 3 {
				t.Fatalf("invalid mode row %q", line)
			}
			for cell := range cells {
				cells[cell] = strings.TrimSpace(cells[cell])
			}
			rows[cells[0]] = cells
		}
		if index+5 < len(lines) && strings.HasPrefix(lines[index+5], "|") {
			t.Fatalf("README mode table has unexpected extra row %q", lines[index+5])
		}
		return rows
	}
	t.Fatal("README has no exact mode table")
	return nil
}

func modeTitle(mode protocol.Mode) string {
	value := string(mode)
	return strings.ToUpper(value[:1]) + value[1:]
}

func formatBindings(bindings []string) string {
	quoted := make([]string, len(bindings))
	for index, binding := range bindings {
		quoted[index] = "`" + binding + "`"
	}
	return strings.Join(quoted, "<br>")
}

func flattenModeRows(rows map[string][]string) []string {
	result := []string{}
	for _, mode := range []string{"Insert", "Normal", "Add"} {
		result = append(result, strings.Join(rows[mode], "|"))
	}
	return result
}

func TestDocumentedPlatformTimeoutsMatchAuthority(t *testing.T) {
	authority, err := os.ReadFile(filepath.Join("..", "internal/candidate/zoxide.go"))
	if err != nil {
		t.Fatal(err)
	}
	readme := readDoc(t, "README.md")
	for _, timeout := range []string{"150 * time.Millisecond", "75 * time.Millisecond"} {
		if !strings.Contains(string(authority), timeout) {
			t.Fatalf("missing timeout authority %s", timeout)
		}
		value := strings.ReplaceAll(strings.ReplaceAll(timeout, " * time.Millisecond", "ms"), " ", "")
		if !strings.Contains(readme, value) {
			t.Errorf("README does not document %s", value)
		}
	}
}

func TestDocumentationMatchesPreviewCacheHandleRelativePrimitives(t *testing.T) {
	document := readDoc(t, "docs/preview.md")
	for _, stale := range []string{"Lstat", "MoveFileExW"} {
		if strings.Contains(document, stale) {
			t.Errorf("preview documentation names stale cache primitive %s", stale)
		}
	}
	claims := []struct {
		document   string
		source     string
		primitives []string
	}{
		{
			document:   "root, component, entry, temporary, publication, timestamp, and prune operations are anchored to opened directory handles",
			source:     "internal/preview/cache_posix.go",
			primitives: []string{"openCacheRoot", "openFileAt", "createFileAt", "unix.Linkat", "unix.Futimes"},
		},
		{
			document:   "`openat`/`mkdirat`",
			source:     "internal/preview/cache_posix.go",
			primitives: []string{"unix.Openat", "unix.Mkdirat", "unix.O_NOFOLLOW"},
		},
		{
			document:   "`linkat`",
			source:     "internal/preview/cache_posix.go",
			primitives: []string{"unix.Linkat", "validateOpenFile(temp, 2", "validateOpenFile(temp, 1"},
		},
		{
			document:   "`renameat`/`unlinkat`-style quarantine and pruning",
			source:     "internal/preview/cache_quarantine_linux.go",
			primitives: []string{"unix.Renameat2", "unix.RENAME_NOREPLACE", "unix.Unlinkat"},
		},
		{
			document:   "stable device/inode identity and expected link count",
			source:     "internal/preview/cache_posix.go",
			primitives: []string{"stat.Dev", "stat.Ino", "stat.Nlink", "info.Mode().IsRegular()"},
		},
		{
			document:   "NT handle-relative `NtCreateFile`",
			source:     "internal/preview/cache_windows_nt.go",
			primitives: []string{"RootDirectory: root", "windows.OBJ_DONT_REPARSE", "windows.FILE_OPEN_REPARSE_POINT", "windows.NtCreateFile"},
		},
		{
			document:   "handle-relative `NtSetInformationFile` publication",
			source:     "internal/preview/cache_windows_nt.go",
			primitives: []string{"info.RootDirectory", "windows.NtSetInformationFile", "windows.FileRenameInformation"},
		},
		{
			document:   "volume/file-index identity and expected link count",
			source:     "internal/preview/cache_windows_nt.go",
			primitives: []string{"info.VolumeSerialNumber", "info.FileIndexHigh", "info.NumberOfLinks", "windows.FILE_ATTRIBUTE_REPARSE_POINT"},
		},
		{
			document:   "handle-based timestamp refresh",
			source:     "internal/preview/cache_windows.go",
			primitives: []string{"windows.SetFileTime", "validateHandle"},
		},
	}
	for _, claim := range claims {
		if !strings.Contains(document, claim.document) {
			t.Errorf("preview documentation missing %q", claim.document)
		}
		source, err := os.ReadFile(filepath.Join("..", claim.source))
		if err != nil {
			t.Fatal(err)
		}
		for _, primitive := range claim.primitives {
			if !strings.Contains(string(source), primitive) {
				t.Errorf("%s does not support documented claim %q: missing %s", claim.source, claim.document, primitive)
			}
		}
	}
}
