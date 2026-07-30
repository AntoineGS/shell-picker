package integration

import (
	"os/exec"
	"strings"
	"testing"
)

func TestModuleGraphExact(t *testing.T) {
	command := exec.Command("go", "list", "-m", "all")
	command.Dir = ".."
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list -m all: %v", err)
	}
	want := map[string]string{
		"github.com/AntoineGS/shell-picker": "",
		"golang.org/x/sys":                  "v0.47.0",
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 1 {
			got[fields[0]] = ""
		} else if len(fields) == 2 {
			got[fields[0]] = fields[1]
		} else {
			t.Fatalf("unexpected module line %q", line)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("module graph=%v, want exactly %v", got, want)
	}
	for module, version := range want {
		if got[module] != version {
			t.Fatalf("module %s version=%q, want %q", module, got[module], version)
		}
	}
}
