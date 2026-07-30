package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
			"case \" $* \" in *\" --print-query \"*) printf '\\000';; esac\nexit 1\n"
	}
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
	lines := strings.Split(document, "\n")
	commands := []string{}
	for index := 0; index < len(lines); index++ {
		if !strings.HasPrefix(lines[index], "```") {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(lines[index], "```"))
		language, _, _ := strings.Cut(info, " ")
		if language != "sh" && language != "bash" && language != "shell" && language != "zsh" {
			continue
		}
		if info != "sh check" {
			t.Fatalf("shell fence %q must be exactly `sh check`", info)
		}
		start := index + 1
		for index++; index < len(lines) && lines[index] != "```"; index++ {
		}
		if index == len(lines) {
			t.Fatal("unterminated sh check block")
		}
		command := strings.TrimSpace(strings.Join(lines[start:index], "\n"))
		if command == "" {
			t.Fatal("empty sh check block")
		}
		commands = append(commands, command)
	}
	return commands
}

func markdownDocuments(t *testing.T) []string {
	t.Helper()
	documents := []string{"README.md"}
	if _, err := os.Stat(filepath.Join("..", "README.md")); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(filepath.Join("..", "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			relative, err := filepath.Rel("..", path)
			if err != nil {
				return err
			}
			documents = append(documents, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
