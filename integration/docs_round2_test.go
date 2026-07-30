package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTwoDocumentationContracts(t *testing.T) {
	checks := map[string][]string{
		"README.md":            {"| Mode | Keys | Result |", "Insert", "Normal", "Add", "Enter creates", "Esc"},
		"docs/protocol.md":     {"generation.discard", "process-error", "preview.finished", "zoxide_max_live", "native", "field-to-event"},
		"docs/architecture.md": {"stateDiagram", "ordinary proposal XOR AddIntent", "pending reply", "cancel → wait → rollback → reply → replacement", "ContainmentForegroundTree", "ContainmentOwnTree", "ContainmentInheritTree", "TreeHandle"},
		"docs/security.md":     {"WaitDelay", "drain", "same-user namespace replacement", "stable pointer identity"},
	}
	for name, values := range checks {
		data, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if !strings.Contains(string(data), value) {
				t.Errorf("%s missing %q", name, value)
			}
		}
	}
}

func TestDocumentedPlatformTimeoutsMatchAuthority(t *testing.T) {
	authority, err := os.ReadFile(filepath.Join("..", "internal/candidate/zoxide.go"))
	if err != nil {
		t.Fatal(err)
	}
	readme := readDoc(t, "README.md")
	for _, timeout := range []string{"150 * time.Millisecond", "75 * time.Millisecond"} {
		if !strings.Contains(string(authority), timeout) {
			t.Fatalf("missing timeout authority %s", timeout)
		}
		value := strings.ReplaceAll(strings.ReplaceAll(timeout, " * time.Millisecond", "ms"), " ", "")
		if !strings.Contains(readme, value) {
			t.Errorf("README does not document %s", value)
		}
	}
}
