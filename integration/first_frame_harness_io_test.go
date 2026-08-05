package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
)

func firstFrameFixture(t *testing.T, binary string, tools firstFrameTools, mode firstFrameMode) (*realFZFFixture, []string, error) {
	t.Helper()
	fixture := newRealFZFFixtureWithPicker(t, tools.fzf, fmt.Sprintf("first-frame-%s", mode), binary)
	dataDir := filepath.Join(fixture.root, "zoxide data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, err
	}
	first := filepath.Join(fixture.home, "zoxide-alpha")
	second := filepath.Join(fixture.home, "zoxide-beta")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, nil, err
		}
	}
	pathEntries := []string{filepath.Dir(tools.zoxide), filepath.Dir(tools.eza), filepath.Dir(tools.fzf), filepath.Dir(fixture.picker), os.Getenv("PATH")}
	environment := replaceEnvironment(withoutFirstFrameDiagnosticEnvironment(os.Environ()), "PATH="+strings.Join(pathEntries, string(os.PathListSeparator)),
		"_ZO_DATA_DIR="+dataDir, "HOME="+fixture.home, "USERPROFILE="+fixture.home, "LOCALAPPDATA="+fixture.home,
		"TERM=xterm-256color", "FZF_DEFAULT_OPTS=--bind=start:abort", fzfsidecar.ActivationVariable+"=0")
	if mode == firstFrameEnabled {
		environment = replaceEnvironment(environment, fzfsidecar.ActivationVariable+"=1")
	}
	for _, path := range []string{first, second} {
		command := exec.Command(tools.zoxide, "add", path)
		command.Env, command.Stdout, command.Stderr = environment, io.Discard, io.Discard
		if err := command.Run(); err != nil {
			return nil, nil, fmt.Errorf("seed real zoxide: %w", err)
		}
	}
	return fixture, environment, nil
}

func writeFirstFrameRaw(t *testing.T, pair int, sample firstFrameSample) {
	t.Helper()
	path := filepath.Join(*firstFrameRawDir, fmt.Sprintf("pair-%03d-%s.json", pair, sample.mode))
	value := firstFrameRawArtifact{Schema: 1, Pair: pair, Mode: sample.mode, SidecarEnabled: sample.mode == firstFrameEnabled,
		Events: sample.events, Metrics: firstFrameMetricsJSONFor(sample.metrics), CallbackThroughFrame: firstFrameCallbackJSONFor(sample.callbackThroughFrame),
		CallbackPreviewComplete: firstFrameCallbackJSONFor(sample.callbackPreviewComplete), ProcessThroughFrame: firstFrameProcessJSONFor(sample.processThroughFrame),
		ProcessPreviewComplete: firstFrameProcessJSONFor(sample.processPreviewComplete), Sidecar: firstFrameSidecarJSONFor(sample.sidecar)}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFirstFrameReport(t *testing.T, report firstFrameRunReport) {
	t.Helper()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(*firstFrameOutput, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
