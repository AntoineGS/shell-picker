package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type artifactFingerprint struct {
	Mode          os.FileMode
	Size          int64
	First, Second uint64
	Hash          [sha256.Size]byte
}

func snapshotArtifacts(t *testing.T, roots []string) map[string]artifactFingerprint {
	t.Helper()
	artifacts := make(map[string]artifactFingerprint)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			first, second, err := artifactIdentity(path, info)
			if err != nil {
				return err
			}
			fingerprint := artifactFingerprint{Mode: info.Mode(), Size: info.Size(), First: first, Second: second}
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				fingerprint.Hash = sha256.Sum256(data)
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				fingerprint.Hash = sha256.Sum256([]byte(target))
			}
			artifacts[path] = fingerprint
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot artifacts beneath %q: %v", root, err)
		}
	}
	return artifacts
}

func awaitResourcesReturned(t *testing.T, baseline resourceSnapshot, timeout time.Duration, roots ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	last := "resources not sampled"
	for {
		current := snapshotResources(t, roots...)
		platformDiff := platformResourceDifference(baseline, current)
		artifactsEqual := reflect.DeepEqual(baseline.artifacts, current.artifacts)
		if platformDiff == "" && artifactsEqual {
			if current.goroutines != baseline.goroutines {
				t.Logf("global runtime goroutines baseline=%d current=%d; owned goroutines joined exactly", baseline.goroutines, current.goroutines)
			}
			return
		}
		last = fmt.Sprintf("platform=%s artifacts_equal=%v", platformDiff, artifactsEqual)
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("owned resources did not quiesce: %s", last)
		}
	}
}

func TestResourceSnapshotFingerprintsArtifactReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotArtifacts(t, []string{root})
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	after := snapshotArtifacts(t, []string{root})
	if reflect.DeepEqual(before, after) {
		t.Fatal("same-path, byte-identical replacement escaped resource fingerprint")
	}
}
