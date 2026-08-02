package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsNativeMakeTargetUsesCheckedInRunner(t *testing.T) {
	command := exec.Command("make", "--no-print-directory", "-n", "windows-native")
	command.Dir = ".."
	command.Env = withoutMakeRecursionVariables(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n windows-native: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "go run ./scripts/windowsnative" {
		t.Fatalf("windows-native target=%q", output)
	}
}

func withoutMakeRecursionVariables(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "MAKELEVEL") || strings.EqualFold(key, "MAKEFLAGS") || strings.EqualFold(key, "MFLAGS") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
