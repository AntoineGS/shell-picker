package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDocumentationContracts(t *testing.T) {
	readme := readDoc(t, "README.md")
	requireDocText(t, readme, "shell-picker cd", "shell-picker cp", "--zoxide-policy", "cached", "fresh", "--zoxide-timeout", "75ms", "150ms", "--zoxide-policy fresh --zoxide-timeout 0", "fzf 0.74.1", "external installation/runtime precondition", "normal picker Run does not verify", "older fzf is unsupported", "cooperative stream contract", "stable pointer identity", "nonclosable", "adapters/zsh/shell-picker.plugin.zsh", "adapters/nushell/shell-picker.nu", "MIT")
	requireDocText(t, readDoc(t, "docs/protocol.md"), "e:mi", "e:en", "l:GENERATION", "FZF_CURRENT_ITEM", "KindVirtual", "drives", "ZHJpdmVz", "nonempty canonical payload", "64 KiB", "64 MiB", "1 KiB", "127.0.0.1:0", "RequestURI", "RawQuery", "RawPath", "exactly one Authorization")
	requireDocText(t, readDoc(t, "docs/parity.md"), "371", "365", "codec: 50", "zshrc-cp: 43", "Windows semantic substitutions", "drive root", "UNC share root", "virtual ..", "default cached", "--zoxide-policy fresh --zoxide-timeout 0")
	requireDocText(t, readDoc(t, "docs/adapters.md"), "default cached", "75ms", "150ms", "dynamically selected unused drive")
	requireDocText(t, readDoc(t, "docs/architecture.md"), "pure reduction", "AddIntent", "exactly one immutable snapshot", "Reduce exactly once", "CreateDirectoryTree exactly once", "Apply exactly once", "no unresolved AddIntent", "session cache", "generation-local cache", "one *candidate.Builder", "must not be copied after first use", "cancellation-aware permit", "independent sessions may query concurrently", "no package-global mutex", "cp never queries zoxide", "authoritative target", "Drives", "PthreadSigmask", "SIGTTOU", "exact prior thread mask", "child fd 3")
	requireDocText(t, readDoc(t, "docs/preview.md"), "KindVirtual", "not a filesystem path", "atomic no-replace", "at most one simultaneously live", "at most three sequential", "native fallback starts none")
	requireDocText(t, readDoc(t, "docs/performance.md"), "cached-navigation", "fresh-navigation", "fresh-exact-parity-navigation", "one attempt per completed fresh generation", "zero or one successful start", "per session", "attempts", "starts", "exits", "processes", "zoxide_max_live")
	requireDocText(t, readDoc(t, "docs/security.md"), "pure reduction", "no filesystem inspection", "cloned AddIntent", "ownership transfers to Actor.Apply", "generation completion before rollback", "cached session", "fresh generation", "absolute filesystem paths", "relative row", "malformed soft failure", "virtual token never reaches", "caller cause wins", "no partial records", "process reaped", "cancelled waiter", "without factory or attempt", "per session", "no package-global mutex", "cp", "attempt 1/start 0", "full-record authorization", "virtual records", "navigation only", "preview", "final output", "complete absolute base ancestry", "atomic no-replace", "concurrent namespace replacement by another process", "caller-owned", "stable pointer identity", "never structurally compared", "emergency cleanup", "EV_ERROR", "Data errno", "unsupported Unix", "blocked forever", "outside the resource guarantee", "process-global signal disposition", "cooperative backend", "promptly honor", "cannot forcibly stop", "limit plus one", "connection reuse", "caller-authorized trace sink", "every ancestor and target", "elevated wrappers", "untrusted trace path", "defense in depth")
	requireDocText(t, readDoc(t, "LICENSE"), "MIT License", "Copyright (c) 2026 AntoineGS")
}

func TestDocumentationCheckCommands(t *testing.T) {
	binary := buildDocsBinary(t)
	home := t.TempDir()
	tools := filepath.Join(home, "tools")
	if err := os.Mkdir(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"fzf", "zoxide", "bat", "chafa", "eza", "exiftool", "ffmpeg", "ffmpegthumbnailer", "file", "glow", "gzip", "kitten", "pdftoppm", "tar", "unzip", "xz"} {
		writeFakeTool(t, tools, tool)
	}
	runDocCommand(t, binary, home, "shell-picker --help", true)
	runDocCommand(t, binary, home, "shell-picker version", false)
	output := runDocCommand(t, binary, home, "shell-picker probe --json", false)
	var report map[string]any
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("probe --json: %v", err)
	}
	for _, document := range []string{"README.md", "docs/architecture.md", "docs/protocol.md", "docs/adapters.md", "docs/preview.md", "docs/performance.md", "docs/parity.md", "docs/security.md"} {
		for _, command := range checkCommands(t, readDoc(t, document)) {
			runDocCommand(t, binary, home, command, false)
		}
	}
}

func readDoc(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func requireDocText(t *testing.T, content string, required ...string) {
	t.Helper()
	for _, value := range required {
		if !strings.Contains(content, value) {
			t.Errorf("missing documentation text %q", value)
		}
	}
}

func buildDocsBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "shell-picker")
	command := exec.Command("go", "build", "-o", binary, "./cmd/shell-picker")
	command.Dir = ".."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build docs binary: %v\n%s", err, output)
	}
	return binary
}

func writeFakeTool(t *testing.T, directory, name string) {
	t.Helper()
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '0.74.1\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runDocCommand(t *testing.T, binary, home, command string, allowUsage bool) string {
	t.Helper()
	command = strings.ReplaceAll(command, "shell-picker", shellQuote(binary))
	process := exec.Command("sh", "-c", command)
	process.Env = []string{"HOME=" + home, "PATH=" + filepath.Join(home, "tools") + ":" + os.Getenv("PATH"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache")}
	output, err := process.CombinedOutput()
	if err != nil && !(allowUsage && strings.Contains(string(output), "usage: shell-picker")) {
		t.Fatalf("documented command %q: %v\n%s", command, err, output)
	}
	return string(output)
}

func checkCommands(t *testing.T, document string) []string {
	t.Helper()
	matches := regexp.MustCompile("(?s)```(?:sh|bash) check\\n(.*?)```").FindAllStringSubmatch(document, -1)
	commands := make([]string, 0, len(matches))
	for _, match := range matches {
		command := strings.TrimSpace(match[1])
		if command == "" {
			t.Fatal("empty check command block")
		}
		commands = append(commands, command)
	}
	return commands
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\\"'\\\"'") + "'"
}
