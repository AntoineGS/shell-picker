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

func workflowJob(t *testing.T, text, name, next string) string {
	t.Helper()
	startMarker := "\n  " + name + ":\n"
	endMarker := "\n  " + next + ":\n"
	start := strings.Index(text, startMarker)
	end := strings.Index(text, endMarker)
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("workflow job boundaries missing or out of order for %q before %q", name, next)
	}
	return text[start+1 : end]
}

type workflowLine struct {
	indent int
	text   string
}

type parsedWorkflow struct {
	jobs map[string]parsedWorkflowJob
}

type parsedWorkflowJob struct {
	matrixOS []string
	runsOn   string
	needs    []string
	steps    []parsedWorkflowStep
}

type parsedWorkflowStep struct {
	name            string
	ifCondition     string
	shell           string
	run             string
	continueOnError string
}

func parseWorkflow(t *testing.T, text string) parsedWorkflow {
	t.Helper()
	lines := workflowLines(t, text)
	jobsIndex := -1
	for index, line := range lines {
		if line.indent == 0 && line.text == "jobs:" {
			jobsIndex = index
			break
		}
	}
	if jobsIndex < 0 {
		t.Fatal("workflow jobs mapping is missing")
	}

	jobStarts := make([]int, 0)
	for index := jobsIndex + 1; index < len(lines); index++ {
		line := lines[index]
		if line.indent <= 0 {
			break
		}
		if line.indent == 2 && strings.HasSuffix(line.text, ":") && !strings.HasPrefix(line.text, "-") {
			jobStarts = append(jobStarts, index)
		}
	}
	if len(jobStarts) == 0 {
		t.Fatal("workflow jobs mapping has no jobs")
	}

	workflow := parsedWorkflow{jobs: make(map[string]parsedWorkflowJob, len(jobStarts))}
	for index, start := range jobStarts {
		end := len(lines)
		if index+1 < len(jobStarts) {
			end = jobStarts[index+1]
		}
		name := strings.TrimSuffix(strings.TrimSpace(lines[start].text), ":")
		workflow.jobs[name] = parseWorkflowJob(t, lines, start, end)
	}
	return workflow
}

func workflowLines(t *testing.T, text string) []workflowLine {
	t.Helper()
	var lines []workflowLine
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if strings.HasPrefix(raw, "\t") {
			t.Fatal("workflow contains tab-indented YAML")
		}
		lines = append(lines, workflowLine{indent: indent, text: strings.TrimSpace(raw)})
	}
	return lines
}

func parseWorkflowJob(t *testing.T, lines []workflowLine, start, end int) parsedWorkflowJob {
	t.Helper()
	job := parsedWorkflowJob{}
	for index := start + 1; index < end; index++ {
		line := lines[index]
		if line.indent != 4 {
			continue
		}
		key, value, ok := workflowMapping(line.text)
		if !ok {
			continue
		}
		switch key {
		case "needs":
			job.needs = workflowList(value)
		case "runs-on":
			job.runsOn = value
		case "strategy":
			job.matrixOS = parseWorkflowMatrix(lines, index, end)
		case "steps":
			job.steps = parseWorkflowSteps(t, lines, index, end)
		}
	}
	return job
}

func parseWorkflowMatrix(lines []workflowLine, start, end int) []string {
	strategyEnd := end
	for index := start + 1; index < end; index++ {
		if lines[index].indent <= 4 {
			strategyEnd = index
			break
		}
	}
	for index := start + 1; index < strategyEnd; index++ {
		line := lines[index]
		if line.indent != 6 {
			continue
		}
		key, value, ok := workflowMapping(line.text)
		if !ok || key != "matrix" {
			continue
		}
		if strings.Contains(value, "os:") {
			return workflowList(strings.TrimSpace(value[strings.Index(value, "os:")+len("os:"):]))
		}
		for nested := index + 1; nested < strategyEnd && lines[nested].indent > 6; nested++ {
			if lines[nested].indent != 8 {
				continue
			}
			nestedKey, nestedValue, nestedOK := workflowMapping(lines[nested].text)
			if nestedOK && nestedKey == "os" {
				return workflowList(nestedValue)
			}
		}
	}
	return nil
}

func parseWorkflowSteps(t *testing.T, lines []workflowLine, start, end int) []parsedWorkflowStep {
	t.Helper()
	steps := make([]parsedWorkflowStep, 0)
	for index := start + 1; index < end; {
		line := lines[index]
		if line.indent != 6 || !strings.HasPrefix(line.text, "-") {
			index++
			continue
		}
		step := parsedWorkflowStep{}
		if key, value, ok := workflowMapping(strings.TrimSpace(strings.TrimPrefix(line.text, "-"))); ok {
			assignWorkflowStepField(&step, key, value)
		}
		index++
		for index < end && lines[index].indent > 6 {
			field := lines[index]
			if field.indent != 8 {
				index++
				continue
			}
			key, value, ok := workflowMapping(field.text)
			if !ok {
				index++
				continue
			}
			if key == "run" && value == "|" {
				var body []string
				index++
				for index < end && lines[index].indent > 8 {
					body = append(body, lines[index].text)
					index++
				}
				assignWorkflowStepField(&step, key, strings.Join(body, "\n"))
				continue
			}
			assignWorkflowStepField(&step, key, value)
			index++
		}
		steps = append(steps, step)
	}
	return steps
}

func workflowMapping(text string) (string, string, bool) {
	colon := strings.IndexByte(text, ':')
	if colon < 0 {
		return "", "", false
	}
	return strings.TrimSpace(text[:colon]), strings.TrimSpace(text[colon+1:]), true
}

func workflowList(value string) []string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil
	}
	items := strings.Split(value[1:len(value)-1], ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		item = strings.Trim(item, "'\"")
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func assignWorkflowStepField(step *parsedWorkflowStep, key, value string) {
	switch key {
	case "name":
		step.name = value
	case "if":
		step.ifCondition = value
	case "shell":
		step.shell = value
	case "run":
		step.run = value
	case "continue-on-error":
		step.continueOnError = value
	}
}

func requiredWorkflowJob(t *testing.T, workflow parsedWorkflow, name string) parsedWorkflowJob {
	t.Helper()
	job, ok := workflow.jobs[name]
	if !ok {
		t.Fatalf("workflow job %q is missing", name)
	}
	return job
}

func parsedWorkflowStepByName(t *testing.T, job parsedWorkflowJob, name string) parsedWorkflowStep {
	t.Helper()
	for _, step := range job.steps {
		if step.name == name {
			return step
		}
	}
	t.Fatalf("workflow job is missing step %q", name)
	return parsedWorkflowStep{}
}

func parsedWorkflowStepContainingRun(t *testing.T, job parsedWorkflowJob, value string) parsedWorkflowStep {
	t.Helper()
	for _, step := range job.steps {
		if strings.Contains(step.run, value) {
			return step
		}
	}
	t.Fatalf("workflow job has no step containing run %q", value)
	return parsedWorkflowStep{}
}

func requireWorkflowValue(t *testing.T, got, want, description string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s=%q want %q", description, got, want)
	}
}

func requireWorkflowListValue(t *testing.T, values []string, want, description string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%s missing %q; got %q", description, want, values)
}

func TestCIWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "ci.yml")
	requireAll(t, text,
		"actions/checkout@v5", "actions/setup-go@v6", "actions/upload-artifact@v4",
		"go-version: 1.26.5", "ubuntu-24.04", "windows-2025", "go test -race", "if: ${{ always() }}",
		"goos: linux", "goos: windows", "goarch: amd64", "goarch: arm64",
		"GOOS: '${{ matrix.goos }}'", "GOARCH: '${{ matrix.goarch }}'", "0.113.1", "adapters-windows",
		"TestNushellAdapter", "TestInstalledFZFCheckVersion", "needs.adapters-windows.result", "needs.fzf-version.result",
		"zsh adapters/zsh/shell-picker.plugin.test.zsh",
		"golang.org/x/sys v0.47.0")
	rejectAll(t, text, "continue-on-error: true", "--listen", "go get",
		"GOOS: linux", "GOOS: windows", "GOARCH: amd64", "GOARCH: arm64",
		"zsh adapters/zsh/shell-picker.test.zsh")
	requireAll(t, text, "go list -m all", "needs.cross-build.result")
	requireAll(t, text, "SHELL_PICKER_REAL_FZF=", "TestModuleGraphExact")
	requireAll(t, text, "Verify make build", "make build", "./bin/shell-picker version",
		"Verify make install", "GOBIN=\"$install_dir\" make install", "\"$install_dir/shell-picker\" version")
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

func TestCIWindowsNativeTopology(t *testing.T) {
	text := readWorkflow(t, "ci.yml")
	unit := workflowJob(t, text, "unit", "windows-native")
	if !strings.Contains(unit, "runs-on: ubuntu-24.04") || strings.Contains(unit, "windows-2025") {
		t.Fatalf("unit is not Linux-only:\n%s", unit)
	}
	windows := workflowJob(t, text, "windows-native", "race-linux")
	requireAll(t, windows, "runs-on: windows-2025", "go run ./scripts/windowsnative")
	rejectAll(t, windows, "go test ./...", "continue-on-error", "if: false", "zsh ", "security-gate.sh")
	requireAll(t, text, "needs.windows-native.result")
}

func TestCIWindowsFZFSmokeContract(t *testing.T) {
	workflow := parseWorkflow(t, readWorkflow(t, "ci.yml"))
	fzfJob := requiredWorkflowJob(t, workflow, "fzf-version")
	requireWorkflowValue(t, fzfJob.runsOn, "${{ matrix.os }}", "fzf-version runner")
	requireWorkflowListValue(t, fzfJob.matrixOS, "windows-2025", "fzf-version matrix")
	requireWorkflowListValue(t, fzfJob.matrixOS, "ubuntu-24.04", "fzf-version matrix")

	prerequisites := parsedWorkflowStepByName(t, fzfJob, "Verify Windows pseudo console prerequisites")
	requireWorkflowValue(t, prerequisites.ifCondition, "runner.os == 'Windows'", "Windows prerequisite condition")

	selection := parsedWorkflowStepByName(t, fzfJob, "Validate Windows real-fzf test selection")
	requireWorkflowValue(t, selection.ifCondition, "runner.os == 'Windows'", "Windows test-list condition")
	requireWorkflowValue(t, selection.shell, "bash", "Windows test-list shell")
	requireAll(t, selection.run,
		"go test ./integration -list '^TestRealFZFInteractiveAbort$' -count=1",
		"grep -Fxq 'TestRealFZFInteractiveAbort'",
	)

	smoke := parsedWorkflowStepByName(t, fzfJob, "Smoke real fzf abort on Windows")
	requireWorkflowValue(t, smoke.ifCondition, "runner.os == 'Windows'", "Windows smoke condition")
	requireWorkflowValue(t, smoke.shell, "bash", "Windows smoke shell")
	requireWorkflowValue(t, smoke.run,
		`SHELL_PICKER_REAL_FZF="$PWD/.ci/fzf/fzf.exe" go test ./integration -run '^TestRealFZFInteractiveAbort$' -count=1 -v`,
		"Windows smoke command")

	for _, step := range fzfJob.steps {
		if strings.Contains(step.run, "TestRealFZFPickerNavigationAndNormalMode") || step.continueOnError == "true" {
			t.Fatalf("fzf-version contains an out-of-scope or non-blocking step: %+v", step)
		}
	}

	required := requiredWorkflowJob(t, workflow, "required")
	requireWorkflowListValue(t, required.needs, "fzf-version", "required.needs")
	rejectFailed := parsedWorkflowStepByName(t, required, "Reject failed required jobs")
	requireAll(t, rejectFailed.run, `test "${{ needs.fzf-version.result }}" = success`)
}

func TestRealFZFWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "real-fzf.yml")
	requireAll(t, text, "workflow_dispatch", "17 3 * * 0", "fzf_version", "0.74.1", "ubuntu-24.04", "windows-2025",
		"TestPlatformPrerequisites", "version_at_least")
	workflow := parseWorkflow(t, text)
	realFZFJob := requiredWorkflowJob(t, workflow, "real-fzf")
	smoke := parsedWorkflowStepContainingRun(t, realFZFJob, "go test ./integration -run TestRealFZF")
	requireWorkflowValue(t, smoke.shell, "bash", "scheduled real-fzf shell")
	requireAll(t, smoke.run, "SHELL_PICKER_REAL_FZF=", "go test ./integration -run TestRealFZF -count=1 -v")
	requireAll(t, readRepositoryFiles(t, "fzf_real_test.go", "fzf_real_navigation_test.go"),
		"func TestRealFZFInteractiveAbort(",
		"func TestRealFZFInteractiveModesReloadAddAccept(",
		"func TestRealFZFPickerNavigationAndNormalMode(",
	)
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
		"navigation-local-only", "baseline-required")
	rejectAll(t, text, "cached-navigation", "fresh-navigation", "fresh-exact-parity-navigation")
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

func TestBuildAndInstallMakeTargets(t *testing.T) {
	command := exec.Command("make", "-n", "build", "install")
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n build install: %v\n%s", err, output)
	}
	requireAll(t, string(output),
		"mkdir -p bin",
		"go build -trimpath -o bin/shell-picker ./cmd/shell-picker",
		"go install -trimpath ./cmd/shell-picker",
	)
}
