package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
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
	if diff := awaitResourceDifference(t, baseline, timeout, roots...); diff != "" {
		t.Fatalf("owned resources did not quiesce: %s", diff)
	}
}

func awaitResourceDifference(t *testing.T, baseline resourceSnapshot, timeout time.Duration, roots ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	last, consecutive := "resources not sampled", 0
	for {
		current := snapshotResources(t, roots...)
		last = resourceDifference(baseline, current)
		if last == "" {
			consecutive++
			if consecutive == 3 {
				return ""
			}
		} else {
			consecutive = 0
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return last
		}
	}
}

func resourceDifference(baseline, current resourceSnapshot) string {
	if diff := platformResourceDifference(baseline, current); diff != "" {
		return "platform=" + diff
	}
	if !reflect.DeepEqual(baseline.artifacts, current.artifacts) {
		return "artifact identities differ"
	}
	var extras []string
	for id, signature := range current.goroutineStacks {
		if _, existed := baseline.goroutineStacks[id]; !existed {
			digest := sha256.Sum256([]byte(signature))
			firstFrame := strings.SplitN(signature, "\n", 3)
			frame := firstFrame[0]
			if len(firstFrame) > 1 {
				frame = firstFrame[1]
			}
			extras = append(extras, fmt.Sprintf("id=%d:%x:%s", id, digest[:8], frame))
		}
	}
	if len(extras) != 0 {
		sort.Strings(extras)
		return "new goroutine identities " + strings.Join(extras, ",")
	}
	return ""
}

var (
	goroutineID  = regexp.MustCompile(`^goroutine ([0-9]+) `)
	stackAddress = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	stackLine    = regexp.MustCompile(`:[0-9]+`)
)

func snapshotGoroutineStacks() map[uint64]string {
	size := 1 << 20
	for {
		buffer := make([]byte, size)
		written := runtime.Stack(buffer, true)
		if written < len(buffer) {
			stacks := make(map[uint64]string)
			for _, stack := range strings.Split(strings.TrimSpace(string(buffer[:written])), "\n\n") {
				if strings.Contains(stack, "integration.snapshotGoroutineStacks") {
					continue // Exclude the observer goroutine whose caller differs by sample site.
				}
				match := goroutineID.FindStringSubmatch(stack)
				if len(match) != 2 {
					continue
				}
				id, err := strconv.ParseUint(match[1], 10, 64)
				if err != nil {
					continue
				}
				normalized := goroutineID.ReplaceAllString(stack, "goroutine # ")
				normalized = stackAddress.ReplaceAllString(normalized, "0x#")
				normalized = stackLine.ReplaceAllString(normalized, ":#")
				stacks[id] = normalized
			}
			return stacks
		}
		size *= 2
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

func TestResourceSnapshotDetectsDescriptorFreeBlockedGoroutine(t *testing.T) {
	baseline := snapshotResources(t)
	block := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		<-block
	}()
	<-started
	diff := awaitResourceDifference(t, baseline, 75*time.Millisecond)
	close(block)
	<-done
	if diff == "" {
		t.Fatal("descriptor-free blocked goroutine escaped resource comparison")
	}
}

func TestResourceSnapshotDetectsSameSignatureGoroutineReplacement(t *testing.T) {
	baseline := resourceSnapshot{goroutineStacks: map[uint64]string{41: "same signature"}}
	current := resourceSnapshot{goroutineStacks: map[uint64]string{42: "same signature"}}
	if diff := resourceDifference(baseline, current); diff == "" {
		t.Fatal("same-signature goroutine replacement escaped identity comparison")
	}
}
