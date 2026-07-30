package integration

import (
	"os"
	"path/filepath"
	"testing"
)

func snapshotArtifacts(t *testing.T, roots []string) map[string]struct{} {
	t.Helper()
	artifacts := make(map[string]struct{})
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != root {
				artifacts[path] = struct{}{}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot artifacts beneath %q: %v", root, err)
		}
	}
	return artifacts
}

func assertArtifactsEqual(t *testing.T, baseline, current map[string]struct{}) {
	t.Helper()
	for path := range current {
		if _, existed := baseline[path]; !existed {
			t.Errorf("owned temporary/cache artifact remained: %s", path)
		}
	}
}
