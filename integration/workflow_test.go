package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", ".github", "workflows", name)
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow %s: %v", name, err)
	}
	return string(text)
}

func requireAll(t *testing.T, text string, required ...string) {
	t.Helper()
	for _, value := range required {
		if !strings.Contains(text, value) {
			t.Errorf("workflow missing %q", value)
		}
	}
}

func rejectAll(t *testing.T, text string, rejected ...string) {
	t.Helper()
	for _, value := range rejected {
		if strings.Contains(text, value) {
			t.Errorf("workflow contains rejected %q", value)
		}
	}
}

func TestCIWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "ci.yml")
	requireAll(t, text,
		"actions/checkout@v5", "actions/setup-go@v6", "actions/upload-artifact@v4", "actions/download-artifact@v4",
		"go-version: 1.26.5", "ubuntu-24.04", "windows-2025", "go test -race", "if: ${{ always() }}",
		"GOOS: linux", "GOOS: windows", "GOARCH: amd64", "GOARCH: arm64", "0.113.1", "adapters-windows",
		"TestNushellAdapter", "TestInstalledFZFCheckVersion", "needs.adapters-windows.result", "needs.fzf-version.result",
		"golang.org/x/sys v0.47.0")
	rejectAll(t, text, "continue-on-error: true", "--listen", "go get")
	requireAll(t, text, "go list -m all", "needs.cross-build.result")
	start := strings.Index(text, "  adapters-windows:")
	end := strings.Index(text, "  fzf-version:")
	if start >= 0 && end > start && strings.Contains(text[start:end], "zsh") {
		t.Fatal("Windows adapter job invokes Zsh")
	}
	if strings.Contains(text, "performance-dedicated") || strings.Contains(text, "-bench") {
		t.Fatal("stable CI invokes a dedicated wall-time benchmark")
	}
}

func TestRealFZFWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "real-fzf.yml")
	requireAll(t, text, "workflow_dispatch", "17 3 * * 0", "fzf_version", "0.74.1", "ubuntu-24.04", "windows-2025",
		"SHELL_PICKER_REAL_FZF", "TestRealFZF", "/dev/ptmx", "TIOCSPTLCK", "TIOCGPTN", "pidfd_open", "17763", "CreatePseudoConsole")
	rejectAll(t, text, "continue-on-error: true")
}

func TestPerformanceWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "performance.yml")
	requireAll(t, text, "workflow_dispatch", "self-hosted", "shell-picker-perf", "SHELL_PICKER_DEDICATED_PERF: 1",
		"cached-navigation", "fresh-navigation", "fresh-exact-parity-navigation", "baseline-required")
	if strings.Contains(text, "pull_request:") || strings.Contains(text, "push:") || strings.Contains(text, "ubuntu-24.04") {
		t.Fatal("performance workflow has an unsupported trigger or runner")
	}
}
