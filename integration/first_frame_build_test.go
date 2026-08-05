package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var firstFrameReproducibleBuildFlags = []string{"-buildvcs=false", "-trimpath", "-ldflags=-buildid="}

const firstFrameDiagnosticEnvironment = "SHELL_PICKER_FIRST_FRAME_DIAGNOSTIC"

type firstFrameBuildMetadata struct {
	Schema         int      `json:"schema"`
	BuildFlags     []string `json:"build_flags"`
	ProductionHash string   `json:"production_sha256"`
	HarnessHash    string   `json:"harness_sha256"`
	SourceHead     string   `json:"source_head"`
	SourceHash     string   `json:"source_fingerprint"`
	StableBuilds   int      `json:"stable_builds"`
	FilesPresent   bool     `json:"files_present"`
	DefenderState  string   `json:"defender_state"`
}

func validateFirstFrameBuildMetadata(metadata firstFrameBuildMetadata) error {
	if metadata.Schema != 1 {
		return fmt.Errorf("first-frame build metadata schema=%d, want 1", metadata.Schema)
	}
	if len(metadata.BuildFlags) != len(firstFrameReproducibleBuildFlags) {
		return fmt.Errorf("first-frame build flags=%v, want %v", metadata.BuildFlags, firstFrameReproducibleBuildFlags)
	}
	for index, flag := range firstFrameReproducibleBuildFlags {
		if metadata.BuildFlags[index] != flag {
			return fmt.Errorf("first-frame build flags=%v, want %v", metadata.BuildFlags, firstFrameReproducibleBuildFlags)
		}
	}
	for name, fingerprint := range map[string]string{
		"production": metadata.ProductionHash,
		"harness":    metadata.HarnessHash,
	} {
		if !validFirstFrameFingerprint(fingerprint) {
			return fmt.Errorf("first-frame %s fingerprint=%q is not a SHA-256 fingerprint", name, fingerprint)
		}
	}
	if !validFirstFrameSourceHead(metadata.SourceHead) {
		return fmt.Errorf("first-frame source HEAD=%q is not a commit hash", metadata.SourceHead)
	}
	if !validFirstFrameFingerprint(metadata.SourceHash) {
		return fmt.Errorf("first-frame source fingerprint=%q is not a SHA-256 fingerprint", metadata.SourceHash)
	}
	if metadata.StableBuilds < 3 {
		return fmt.Errorf("first-frame stable build count=%d, want at least 3", metadata.StableBuilds)
	}
	if !metadata.FilesPresent {
		return errors.New("first-frame build outputs were not present after verification")
	}
	if strings.TrimSpace(metadata.DefenderState) == "" {
		return errors.New("first-frame Defender state is missing")
	}
	return nil
}

func validFirstFrameFingerprint(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validFirstFrameSourceHead(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func readFirstFrameBuildMetadata(path string) (firstFrameBuildMetadata, error) {
	if path == "" {
		return firstFrameBuildMetadata{}, errors.New("first-frame build metadata path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return firstFrameBuildMetadata{}, fmt.Errorf("read first-frame build metadata: %w", err)
	}
	var metadata firstFrameBuildMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return firstFrameBuildMetadata{}, fmt.Errorf("decode first-frame build metadata: %w", err)
	}
	if err := validateFirstFrameBuildMetadata(metadata); err != nil {
		return firstFrameBuildMetadata{}, err
	}
	return metadata, nil
}

func requireFirstFrameBuildQualification(t testingT, production string, metadataPath string) firstFrameBuildMetadata {
	t.Helper()
	metadata, err := readFirstFrameBuildMetadata(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(production); err != nil {
		t.Fatalf("verified first-frame production binary is not present: %v", err)
	}
	productionHash := firstFrameBinaryFingerprintForPath(production)
	if productionHash != metadata.ProductionHash {
		t.Fatalf("first-frame production fingerprint=%s, verified build=%s", productionHash, metadata.ProductionHash)
	}
	harness, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve first-frame harness: %v", err)
	}
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("verified first-frame harness is not present: %v", err)
	}
	harnessHash := firstFrameBinaryFingerprintForPath(harness)
	if harnessHash != metadata.HarnessHash {
		t.Fatalf("first-frame harness fingerprint=%s, verified build=%s", harnessHash, metadata.HarnessHash)
	}
	head, sourceHash, err := firstFrameSourceFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if head != metadata.SourceHead || sourceHash != metadata.SourceHash {
		t.Fatalf("first-frame source identity=%s/%s, verified build=%s/%s", head, sourceHash, metadata.SourceHead, metadata.SourceHash)
	}
	return metadata
}

type testingT interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

func firstFrameBinaryFingerprintForPath(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return firstFrameSHA256(data)
}

func firstFrameSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func firstFrameDiagnosticMode(getenv func(string) string) bool {
	return getenv(firstFrameDiagnosticEnvironment) == "1"
}

func firstFrameMeasurementDecision(baselineStatus string, diagnostic bool) (bool, string) {
	if baselineStatus == "qualified" {
		return true, "qualified"
	}
	if diagnostic {
		return true, "diagnostic-unqualified"
	}
	return false, baselineStatus
}

func withoutFirstFrameDiagnosticEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, firstFrameDiagnosticEnvironment) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func firstFrameSourceFingerprint() (string, string, error) {
	root, err := firstFrameRepositoryRoot()
	if err != nil {
		return "", "", err
	}
	headCommand := exec.Command("git", "rev-parse", "HEAD")
	headCommand.Dir = root
	headBytes, err := headCommand.Output()
	if err != nil {
		return "", "", fmt.Errorf("read source HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))
	if len(head) != 40 {
		return "", "", fmt.Errorf("source HEAD=%q is not a commit hash", head)
	}
	listCommand := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "--",
		"cmd", "internal", "integration", "go.mod", "go.sum", "Makefile", "scripts/verify-first-frame-build.ps1")
	listCommand.Dir = root
	listBytes, err := listCommand.Output()
	if err != nil {
		return "", "", fmt.Errorf("list source inputs: %w", err)
	}
	paths := make([]string, 0)
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(listBytes)), "\n") {
		path := filepath.ToSlash(strings.TrimSpace(line))
		if path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	manifest := strings.Builder{}
	manifest.WriteString(head)
	manifest.WriteByte('\n')
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return "", "", fmt.Errorf("read source input %q: %w", path, err)
		}
		digest := sha256.Sum256(data)
		manifest.WriteString(path)
		manifest.WriteByte('\t')
		manifest.WriteString(hex.EncodeToString(digest[:]))
		manifest.WriteByte('\n')
	}
	return head, firstFrameSHA256([]byte(manifest.String())), nil
}

func firstFrameRepositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not locate repository root")
		}
		current = parent
	}
}

func TestFirstFrameDiagnosticModeIsExplicitAndNeverInheritedByChildren(t *testing.T) {
	if !firstFrameDiagnosticMode(func(string) string { return "1" }) {
		t.Fatal("explicit diagnostic environment was not recognized")
	}
	if firstFrameDiagnosticMode(func(string) string { return "0" }) {
		t.Fatal("disabled diagnostic environment was recognized")
	}
	child := withoutFirstFrameDiagnosticEnvironment([]string{"KEEP=yes", "SHELL_PICKER_FIRST_FRAME_DIAGNOSTIC=1"})
	if strings.Contains(strings.Join(child, "\x00"), "SHELL_PICKER_FIRST_FRAME_DIAGNOSTIC") {
		t.Fatalf("diagnostic environment reached child: %q", child)
	}
	if run, status := firstFrameMeasurementDecision("baseline-required", false); run || status != "baseline-required" {
		t.Fatalf("official baseline decision=%t/%q, want false/baseline-required", run, status)
	}
	if run, status := firstFrameMeasurementDecision("baseline-required", true); !run || status != "diagnostic-unqualified" {
		t.Fatalf("diagnostic baseline decision=%t/%q, want true/diagnostic-unqualified", run, status)
	}
	if run, status := firstFrameMeasurementDecision("baseline-required", false); run || status == "pass" {
		t.Fatalf("official first-frame mode emitted a passing status: run=%t status=%q", run, status)
	}
	if run, status := firstFrameMeasurementDecision("baseline-required", true); !run || status == "pass" {
		t.Fatalf("diagnostic first-frame mode emitted a passing status: run=%t status=%q", run, status)
	}
}

func TestFirstFrameSourceFingerprintIsStableAndIncludesHead(t *testing.T) {
	head, fingerprint, err := firstFrameSourceFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 40 || !validFirstFrameFingerprint(fingerprint) {
		t.Fatalf("source identity head=%q fingerprint=%q", head, fingerprint)
	}
	secondHead, secondFingerprint, err := firstFrameSourceFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if head != secondHead || fingerprint != secondFingerprint {
		t.Fatalf("source identity is not deterministic: first=%s/%s second=%s/%s", head, fingerprint, secondHead, secondFingerprint)
	}
}

func TestFirstFrameBuildScriptParsesInPowerShell(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skipf("PowerShell is unavailable: %v", err)
	}
	root, err := firstFrameRepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "verify-first-frame-build.ps1")
	quotedScript := strings.ReplaceAll(script, "'", "''")
	command := exec.Command(pwsh, "-NoProfile", "-Command", `$tokens = $null; $errors = $null; [System.Management.Automation.Language.Parser]::ParseFile('`+quotedScript+`', [ref]$tokens, [ref]$errors) | Out-Null; if ($errors.Count -gt 0) { $errors | ForEach-Object { $_.Message }; exit 1 }`)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell parser rejected build script: %v\n%s", err, output)
	}
}

func TestFirstFrameBuildScriptRejectsSourceMutationAndRemovesOutputs(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skipf("PowerShell is unavailable: %v", err)
	}
	root, err := firstFrameRepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	write := func(relative, contents string) {
		t.Helper()
		path := filepath.Join(repository, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cmd/shell-picker/main.go", "package main\n")
	write("internal/placeholder.go", "package internal\n")
	write("integration/placeholder_test.go", "package integration\n")
	write("go.mod", "module example.test/shell-picker\n\ngo 1.26\n")
	scriptData, err := os.ReadFile(filepath.Join(root, "scripts", "verify-first-frame-build.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/verify-first-frame-build.ps1", string(scriptData))
	fakeGo := filepath.Join(repository, "fake-bin", "go.cmd")
	write("fake-bin/go.cmd", `@echo off
if not exist "%~dp0mutated.marker" (
  >>"%REPO%\cmd\shell-picker\main.go" echo // changed during build
  >"%~dp0mutated.marker" echo changed
)
set "output="
:parse
if "%~1"=="" goto write
if /I "%~1"=="-o" set "output=%~2"
shift
goto parse
:write
if not defined output exit /b 2
>"%output%" echo mock
exit /b 0
`)
	run := func(arguments ...string) {
		t.Helper()
		command := exec.Command(arguments[0], arguments[1:]...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@example.invalid")
	run("git", "config", "user.name", "First Frame Test")
	run("git", "add", ".")
	run("git", "commit", "-qm", "initial")

	production := filepath.Join(repository, "outputs", "picker.exe")
	harness := filepath.Join(repository, "outputs", "harness.test.exe")
	metadata := filepath.Join(repository, "outputs", "build.json")
	if err := os.MkdirAll(filepath.Dir(production), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{production, harness, metadata} {
		if err := os.WriteFile(path, []byte("stale output"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(pwsh, "-NoProfile", "-File", filepath.Join(repository, "scripts", "verify-first-frame-build.ps1"),
		"-ProductionOutput", production, "-HarnessOutput", harness, "-MetadataOutput", metadata)
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") || strings.HasPrefix(strings.ToUpper(entry), "REPO=") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "PATH="+filepath.Dir(fakeGo)+";"+os.Getenv("PATH"), "REPO="+repository)
	command.Env = environment
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("source mutation during reproducible build was accepted:\n%s", output)
	}
	if !strings.Contains(string(output), "source changed during reproducible build") {
		t.Fatalf("source mutation failure did not identify source drift: %v\n%s", err, output)
	}
	for _, path := range []string{production, harness, metadata} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("source mismatch left output %q behind: %v", path, statErr)
		}
	}
}
