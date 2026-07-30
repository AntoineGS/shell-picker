package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPerformanceJSONOutputs(t *testing.T) {
	if os.Getenv("SHELL_PICKER_PERFORMANCE_OUTPUTS") != "1" {
		t.Skip("performance workflow output validation only")
	}
	for _, name := range []string{"host-baseline.json", "performance.json"} {
		data, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if len(value) == 0 {
			t.Fatalf("%s is empty JSON object", name)
		}
	}
}
