package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

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
	if report["schema"] != float64(1) || report["ready"] != true {
		t.Fatalf("unexpected probe identity: %#v", report)
	}
	fzf, ok := report["fzf"].(map[string]any)
	if !ok || fzf["status"] != "ready" || fzf["version"] != "0.74.1" {
		t.Fatalf("unexpected fzf probe: %#v", report["fzf"])
	}
	zoxide, ok := report["zoxide"].(map[string]any)
	if !ok || zoxide["status"] != "available" || zoxide["default_policy"] != "cached" || zoxide["exact_parity"] != "--zoxide-policy fresh --zoxide-timeout 0" {
		t.Fatalf("unexpected zoxide probe: %#v", report["zoxide"])
	}
	for _, document := range markdownDocuments(t) {
		content := readDoc(t, document)
		validateCLIFlags(t, binary, home, content)
		for _, command := range checkCommands(t, content) {
			checkShellSyntax(t, command)
			output := runDocCommand(t, binary, home, command, false)
			if isPickerDocCommand(command) && output != "" {
				t.Fatalf("documented picker abort command %q produced output %q", command, output)
			}
		}
	}
	invocations, err := os.ReadFile(filepath.Join(home, "fzf-invocations"))
	if err != nil {
		t.Fatal("documented picker commands did not invoke fake fzf")
	}
	if got := len(strings.Split(strings.TrimSpace(string(invocations)), "\n")); got != 2 {
		t.Fatalf("documented picker commands produced %d fake fzf invocations, want 2", got)
	}
}

func TestDocumentationDiscoversEveryExecutableFence(t *testing.T) {
	commands, err := parseCheckCommands(readDoc(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(commands, "\n")
	for _, command := range []string{"shell-picker cd", "shell-picker cp"} {
		if !strings.Contains(joined, command) {
			t.Errorf("README executable command %q was not discovered", command)
		}
	}
}

func TestMarkdownDocumentsExcludesUntrackedMarkdown(t *testing.T) {
	temporary, err := os.CreateTemp(filepath.Join("..", "docs"), "untracked-document-*.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(temporary.Name()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})

	relative, err := filepath.Rel("..", temporary.Name())
	if err != nil {
		t.Fatal(err)
	}
	temporaryDocument := filepath.ToSlash(relative)
	documents := markdownDocuments(t)
	if slices.Contains(documents, temporaryDocument) {
		t.Fatalf("untracked Markdown document %q was discovered", temporaryDocument)
	}
	if !slices.Contains(documents, "docs/protocol.md") {
		t.Fatal("tracked Markdown document docs/protocol.md was not discovered")
	}
}

func TestExecutableCommandCannotHideInTextFence(t *testing.T) {
	_, err := parseCheckCommands("```text\nshell-picker cd --cwd \"$PWD\" --home \"$HOME\" --output nul\n```\n")
	if err == nil {
		t.Fatal("executable command in text fence was accepted")
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
	if name == "fzf" {
		body = "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '0.74.1\\n'; exit 0; fi\n" +
			"printf '%s\\n' \"$*\" >> \"$HOME/fzf-invocations\"\n" +
			"case \" $* \" in *\" --print-query \"*) printf '\\000';; esac\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runDocCommand(t *testing.T, binary, home, command string, allowUsage bool) string {
	t.Helper()
	pickerCommand := isPickerDocCommand(command)
	command = strings.ReplaceAll(command, "shell-picker", shellQuote(binary))
	process := exec.Command("sh", "-c", command)
	process.Env = []string{"HOME=" + home, "PATH=" + filepath.Join(home, "tools") + ":" + os.Getenv("PATH"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache")}
	var output []byte
	var err error
	if pickerCommand {
		output, err = runPickerDocCommand(process)
	} else {
		output, err = process.CombinedOutput()
	}
	if err != nil && !(allowUsage && strings.Contains(string(output), "usage: shell-picker")) {
		t.Fatalf("documented command %q: %v\n%s", command, err, output)
	}
	return string(output)
}

func checkCommands(t *testing.T, document string) []string {
	t.Helper()
	commands, err := parseCheckCommands(document)
	if err != nil {
		t.Fatal(err)
	}
	return commands
}

func parseCheckCommands(document string) ([]string, error) {
	lines := strings.Split(document, "\n")
	commands := []string{}
	for index := 0; index < len(lines); index++ {
		if !strings.HasPrefix(lines[index], "```") {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(lines[index], "```"))
		start := index + 1
		for index++; index < len(lines) && lines[index] != "```"; index++ {
		}
		if index == len(lines) {
			return nil, fmt.Errorf("unterminated fence %q", info)
		}
		command := strings.TrimSpace(strings.Join(lines[start:index], "\n"))
		language, _, _ := strings.Cut(info, " ")
		shell := language == "sh" || language == "bash" || language == "shell" || language == "zsh"
		if !shell {
			if isShellPickerDocCommand(command) {
				return nil, fmt.Errorf("executable command in %q fence must be `sh check`", info)
			}
			continue
		}
		if info != "sh check" {
			return nil, fmt.Errorf("shell fence %q must be exactly `sh check`", info)
		}
		if command == "" {
			return nil, fmt.Errorf("empty sh check block")
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func isPickerDocCommand(command string) bool {
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$"))
		if line == "shell-picker cd" || line == "shell-picker cp" ||
			strings.HasPrefix(line, "shell-picker cd ") || strings.HasPrefix(line, "shell-picker cp ") {
			return true
		}
	}
	return false
}

func isShellPickerDocCommand(command string) bool {
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$"))
		if line == "shell-picker" || strings.HasPrefix(line, "shell-picker ") {
			return true
		}
	}
	return false
}

func markdownDocuments(t *testing.T) []string {
	t.Helper()
	root := filepath.Clean("..")
	list := exec.Command("git", "ls-files", "--cached", "-z", "--", "README.md", "docs")
	list.Dir = root
	output, err := list.Output()
	if err != nil {
		t.Fatalf("list checked-in Markdown documents: %v", err)
	}
	documents := []string{}
	for _, document := range strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00") {
		if document == "" || !strings.EqualFold(filepath.Ext(document), ".md") {
			continue
		}
		if _, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(document))); err != nil {
			t.Fatalf("read %s: %v", document, err)
		}
		documents = append(documents, document)
	}
	if !slices.Contains(documents, "README.md") {
		t.Fatal("README.md is not a checked-in Markdown document")
	}
	sort.Strings(documents)
	return documents
}

func checkShellSyntax(t *testing.T, command string) {
	t.Helper()
	process := exec.Command("sh", "-n")
	process.Stdin = strings.NewReader(command)
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("invalid documented shell syntax: %v\n%s", err, output)
	}
}

func validateCLIFlags(t *testing.T, binary, home, command string) {
	t.Helper()
	for _, line := range strings.Split(command, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "shell-picker" {
			continue
		}
		subcommand := ""
		if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") {
			subcommand = fields[1]
		}
		helpCommand := "shell-picker --help"
		if subcommand != "" {
			helpCommand = "shell-picker " + subcommand + " --help"
		}
		help := runDocCommand(t, binary, home, helpCommand, true)
		for _, field := range fields[1:] {
			if !strings.HasPrefix(field, "--") || field == "--help" {
				continue
			}
			pattern := `(^|[[:space:]|\[])` + regexp.QuoteMeta(field) + `([[:space:]|\]])`
			if !regexp.MustCompile(pattern).MatchString(help) {
				t.Errorf("documented flag %s for %s is absent from CLI usage %q", field, subcommand, help)
			}
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\\"'\\\"'") + "'"
}
