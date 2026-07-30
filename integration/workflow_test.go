package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		"actions/checkout@v5", "actions/setup-go@v6", "actions/upload-artifact@v4",
		"go-version: 1.26.5", "ubuntu-24.04", "windows-2025", "go test -race", "if: ${{ always() }}",
		"GOOS: linux", "GOOS: windows", "GOARCH: amd64", "GOARCH: arm64", "0.113.1", "adapters-windows",
		"TestNushellAdapter", "TestInstalledFZFCheckVersion", "needs.adapters-windows.result", "needs.fzf-version.result",
		"golang.org/x/sys v0.47.0")
	rejectAll(t, text, "continue-on-error: true", "--listen", "go get")
	requireAll(t, text, "go list -m all", "needs.cross-build.result")
	requireAll(t, text, "SHELL_PICKER_REAL_FZF=", "TestModuleGraphExact")
	if strings.Contains(text, "FZF_PATH=") || strings.Contains(text, "if: false") {
		t.Fatal("stable CI contains a disconnected fzf path or disabled action")
	}
	if strings.Contains(text, "name: ${{ matrix.output }}") || strings.Contains(text, "name: '${{ matrix.output }}'") {
		t.Fatal("cross-build artifact name is derived from a path")
	}
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
		"SHELL_PICKER_REAL_FZF", "TestRealFZF", "TestPlatformPrerequisites", "version_at_least")
	prerequisites := readRepositoryFiles(t, "platform_prerequisites_linux_test.go", "platform_prerequisites_windows_test.go")
	requireAll(t, prerequisites, "/dev/ptmx", "TIOCSPTLCK", "TIOCGPTN", "pidfd_open", "17763", "CreatePseudoConsole")
	rejectAll(t, text, "continue-on-error: true")
	if !regexp.MustCompile(`version_at_least|version.*compare|sort -V|SemVer`).MatchString(text) {
		t.Fatal("real-fzf workflow lacks semantic version comparison")
	}
}

func TestPerformanceWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "performance.yml")
	requireAll(t, text, "workflow_dispatch", "self-hosted", "shell-picker-perf", "SHELL_PICKER_DEDICATED_PERF: 1",
		"cached-navigation", "fresh-navigation", "fresh-exact-parity-navigation", "baseline-required")
	if strings.Contains(text, "pull_request:") || strings.Contains(text, "push:") || strings.Contains(text, "ubuntu-24.04") {
		t.Fatal("performance workflow has an unsupported trigger or runner")
	}
	if !strings.Contains(text, "TestPerformanceJSONOutputs") || !strings.Contains(text, "SHELL_PICKER_PERFORMANCE_OUTPUTS: 1") {
		t.Fatal("performance workflow does not enforce real JSON outputs")
	}
}

func readRepositoryFiles(t *testing.T, names ...string) string {
	t.Helper()
	var builder strings.Builder
	for _, name := range names {
		text, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(text)
	}
	return builder.String()
}

func TestSourceLimitContractHasNoAllowlist(t *testing.T) {
	text, err := os.ReadFile("source_limits_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "legacy") || strings.Contains(string(text), "Exceptions") {
		t.Fatal("source-limit contract weakens coverage with an allowlist")
	}
}

func TestGoFormattingCoversEverySourceLimitRoot(t *testing.T) {
	for _, target := range []string{"fmt", "fmt-check"} {
		command := exec.Command("make", "-n", target)
		command.Dir = ".."
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, output)
		}
		if !strings.Contains(string(output), "cmd internal integration scripts") {
			t.Errorf("make %s does not format every source-limit root: %s", target, output)
		}
	}
}
