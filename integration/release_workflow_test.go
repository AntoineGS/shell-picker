package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "release.yml")
	requireAll(t, text, "tags:", "- 'v*'", "actions/checkout@v5", "actions/setup-go@v6", "actions/upload-artifact@v4", "actions/download-artifact@v4", "linux", "windows", "amd64", "arm64", "checksums.txt", "gh release create")
	rejectAll(t, text, "latest", "continue-on-error: true", "--force")
}

func TestInjectedVersion(t *testing.T) {
	binary := buildReleaseCommand(t, `-X main.version=v1.2.3`)
	out := runCommand(t, binary, "version")
	if out != "shell-picker v1.2.3\n" {
		t.Fatalf("out=%q", out)
	}
}

func buildReleaseCommand(t *testing.T, ldflags string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "shell-picker")
	command := exec.Command("go", "build", "-ldflags", ldflags, "-o", binary, "../cmd/shell-picker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

func runCommand(t *testing.T, binary string, args ...string) string {
	t.Helper()
	output, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
	}
	return string(output)
}
