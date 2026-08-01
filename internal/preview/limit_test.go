package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCountingWriterReturnsOutputLimitWithoutExceedingBound(t *testing.T) {
	var destination bytes.Buffer
	writer := newCountingWriter(&destination, 4)
	n, err := writer.Write([]byte("abcdef"))
	if n != 4 || !errors.Is(err, ErrOutputLimit) || destination.String() != "abcd" {
		t.Fatalf("n=%d err=%v output=%q", n, err, destination.String())
	}
	if n, err = writer.Write([]byte("z")); n != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("second write n=%d err=%v", n, err)
	}
}

func TestNormalizedLimitsRestoreSecurityDefaults(t *testing.T) {
	got := normalizedLimits(Limits{})
	if got != DefaultLimits {
		t.Fatalf("got %+v want %+v", got, DefaultLimits)
	}
}

func TestOutputBudgetAggregatesConcurrentDestinations(t *testing.T) {
	var first, second bytes.Buffer
	var limitCalls atomic.Int32
	budget := newOutputBudget(10, func() { limitCalls.Add(1) })
	writers := []*budgetWriter{budget.writer(&first), budget.writer(&second)}
	errorsSeen := make(chan error, len(writers))
	var group sync.WaitGroup
	for _, writer := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := writer.Write([]byte("12345678"))
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	limited := false
	for err := range errorsSeen {
		limited = limited || errors.Is(err, ErrOutputLimit)
	}
	written, exceeded := budget.status()
	if written != 10 || first.Len()+second.Len() != 10 || !exceeded || !limited || limitCalls.Load() != 1 {
		t.Fatalf("written=%d destinations=%d exceeded=%v limited=%v callbacks=%d", written,
			first.Len()+second.Len(), exceeded, limited, limitCalls.Load())
	}
}

type orderedTreeHandle struct{ events *[]string }

func (handle orderedTreeHandle) KillTree() error {
	*handle.events = append(*handle.events, "kill")
	return nil
}
func (orderedTreeHandle) Close() error { return nil }

func TestTerminalRetriesBlockedCleanupAfterTreeKill(t *testing.T) {
	events := []string{}
	attempts := 0
	session := &renderSession{tree: orderedTreeHandle{&events}}
	session.cleanup = func() bool {
		attempts++
		events = append(events, fmt.Sprintf("cleanup-%d", attempts))
		return attempts > 1
	}
	_ = session.terminal(ErrArtifactLimit)
	if strings.Join(events, ",") != "cleanup-1,kill,cleanup-2" || session.cleanup != nil {
		t.Fatalf("events=%v cleanup retained=%v", events, session.cleanup != nil)
	}
}

func TestTerminalBoundsCleanupRetryThenReleasesOwnedHandles(t *testing.T) {
	oldNow, oldSleep := cleanupNow, cleanupSleep
	now := time.Unix(100, 0)
	cleanupNow = func() time.Time { return now }
	cleanupSleep = func(duration time.Duration) { now = now.Add(duration) }
	t.Cleanup(func() { cleanupNow, cleanupSleep = oldNow, oldSleep })
	events := []string{}
	session := &renderSession{tree: orderedTreeHandle{&events}}
	session.cleanup = func() bool { events = append(events, "cleanup"); return false }
	session.abandon = func() { events = append(events, "abandon") }
	_ = session.terminal(ErrArtifactLimit)
	if elapsed := now.Sub(time.Unix(100, 0)); elapsed != time.Second {
		t.Fatalf("retry duration=%v", elapsed)
	}
	if events[0] != "cleanup" || events[1] != "kill" || events[len(events)-1] != "abandon" {
		t.Fatalf("events=%v", events)
	}
	if session.cleanup != nil || session.abandon != nil {
		t.Fatalf("cleanup retained=%v abandon retained=%v", session.cleanup != nil, session.abandon != nil)
	}
}

func TestNewCacheRejectsComponentReplacementAfterCreationWalk(t *testing.T) {
	base := t.TempDir()
	components := []string{base, "anchor", "observed"}
	for index := 0; index < 512; index++ {
		components = append(components, fmt.Sprintf("d%03d", index))
	}
	root := filepath.Join(components...)
	swapped := make(chan error, 1)
	go func() {
		observed := filepath.Join(base, "anchor", "observed")
		for {
			if _, err := os.Stat(observed); err == nil {
				break
			}
			runtime.Gosched()
		}
		anchor := filepath.Join(base, "anchor")
		err := os.Rename(anchor, anchor+"-detached")
		if err == nil {
			err = os.Mkdir(anchor, 0o700)
		}
		swapped <- err
	}()
	cache, err := NewCache(root, 1)
	if swapErr := <-swapped; swapErr != nil {
		t.Fatal(swapErr)
	}
	if err == nil || cache != nil {
		t.Fatalf("detached cache=%+v err=%v", cache, err)
	}
}

func TestPruneQuarantineDoesNotDeleteReplacement(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("7", 64)
	path := mustPut(t, cache, key, "original")
	source, err := openCacheSource(cache, key)
	if err != nil {
		t.Fatal(err)
	}
	identity := source.identity
	_ = source.Close()
	replaceWithDistinctInode(t, path, []byte("attacker"))
	if quarantinePrune(cache, pruneItem{name: key, size: 8, identity: identity}) {
		t.Fatal("prune deleted replacement")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "attacker" {
		t.Fatalf("replacement=%q err=%v", data, err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestStagedArtifactRejectsReplacementAfterExclusiveCreation(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	stage, err := newConverterArtifact(cache, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupConverterArtifact(t, stage) })
	if runtime.GOOS == "windows" {
		if err := stage.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(stage.Path()); err == nil {
			t.Fatal("Windows removed an exclusively held stage")
		}
		return
	}
	replaceWithDistinctInode(t, stage.Path(), []byte("attacker"))
	writer, err := stage.OpenWritable()
	if writer != nil {
		_ = writer.Close()
	}
	if !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("replacement writer error=%v", err)
	}
}

func cleanupConverterArtifact(t *testing.T, artifact *converterArtifact) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !artifact.Cleanup() {
		if time.Now().After(deadline) {
			t.Fatal("converter artifact handles did not close within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func replaceWithDistinctInode(t *testing.T, path string, data []byte) {
	t.Helper()
	replacement, err := os.CreateTemp(filepath.Dir(path), ".shell-picker-replacement-")
	if err != nil {
		t.Fatal(err)
	}
	replacementPath := replacement.Name()
	if _, err := replacement.Write(data); err != nil {
		_ = replacement.Close()
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}
}
func TestStagedArtifactTruncatesValidatedCreationIdentity(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	stage, err := newConverterArtifact(cache, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupConverterArtifact(t, stage) })
	attacker, err := stage.OpenWritable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attacker.Write([]byte("attacker-trailing-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := attacker.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := stage.OpenWritable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("safe")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, _, err := stage.OpenAccepted()
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(data) != "safe" {
		t.Fatalf("data=%q read=%v close=%v", data, readErr, closeErr)
	}
}
func TestRendererReadsValidatedStageWhenPathIsReplaced(t *testing.T) {
	tools := t.TempDir()
	executable := filepath.Join(tools, "chafa")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	source := filepath.Join(t.TempDir(), "renderer.go")
	program := `package main
import("os";"time")
func main(){ os.WriteFile(os.Getenv("READY"),[]byte(os.Args[len(os.Args)-1]),0600); for { if _,e:=os.Stat(os.Getenv("GO")); e==nil { break }; time.Sleep(time.Millisecond) }; b,e:=os.ReadFile(os.Args[len(os.Args)-1]); if e!=nil { panic(e) }; os.Stdout.Write(b) }`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("build renderer: %v: %s", err, output)
	}
	document := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(document, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	candidate := resolved(document)
	mustPut(t, cache, cache.Key(candidate, "pdf-pdftoppm-v1"), "safe")
	ready, proceed := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "go")
	var output bytes.Buffer
	options := testOptions(&output)
	options.Cache, options.Environment = cache, []string{"PATH=" + tools, "READY=" + ready, "GO=" + proceed}
	done := make(chan error, 1)
	go func() { done <- Render(context.Background(), candidate, options) }()
	var argument []byte
	for deadline := time.Now().Add(5 * time.Second); ; {
		argument, _ = os.ReadFile(ready)
		if len(argument) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("renderer did not start")
		}
		time.Sleep(time.Millisecond)
	}
	stagePath := stagedArtifactPath(t, cache.root)
	attackErr := os.Remove(stagePath)
	if attackErr == nil {
		attackErr = os.WriteFile(stagePath, []byte("attacker"), 0o600)
	}
	if runtime.GOOS == "windows" && attackErr == nil {
		t.Fatal("Windows stage replacement succeeded")
	}
	if err := os.WriteFile(proceed, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if output.String() != "safe" {
		t.Fatalf("renderer output=%q attack=%v", output.String(), attackErr)
	}
	if runtime.GOOS == "linux" && string(argument) != "/proc/self/fd/3" {
		t.Fatalf("renderer path=%q", argument)
	}
}

func stagedArtifactPath(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), cacheTempPrefix) {
			continue
		}
		children, err := readStageDirectory(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		artifact, marker := false, false
		for _, child := range children {
			switch child.Name() {
			case "artifact.jpg":
				artifact = true
			case ".shell-picker-owner-v1":
				if runtime.GOOS != "windows" {
					t.Fatalf("unexpected stage marker %q", child.Name())
				}
				marker = true
			default:
				t.Fatalf("unexpected staged entry %q", child.Name())
			}
		}
		if artifact && (runtime.GOOS != "windows" || marker && len(children) == 2) {
			return filepath.Join(root, entry.Name(), "artifact.jpg")
		}
	}
	t.Fatal("staged artifact not found")
	return ""
}

var readStageDirectory = os.ReadDir
