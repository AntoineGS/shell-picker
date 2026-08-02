package integration

import (
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsNativeMakeTargetUsesCheckedInRunner(t *testing.T) {
	command := exec.Command("make", "-n", "windows-native")
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n windows-native: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "go run ./scripts/windowsnative" {
		t.Fatalf("windows-native target=%q", output)
	}
}
