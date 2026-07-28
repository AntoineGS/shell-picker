package candidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type processCounts struct {
	mu                                     sync.Mutex
	attempts, starts, live, maxLive, exits int
}

func (c *processCounts) observe(event process.ProcessEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch event.Phase {
	case "attempt":
		c.attempts++
	case "start":
		c.starts++
		c.live++
		if c.live > c.maxLive {
			c.maxLive = c.live
		}
	case "exit":
		c.exits++
		c.live--
	}
}

func (c *processCounts) values() (int, int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts, c.starts, c.maxLive, c.exits
}

func (c *processCounts) liveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

func zoxideExecutable(t testing.TB, body string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	name := "zoxide-test"
	path := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		path += ".cmd"
		body = "@echo off\r\n" + body
	} else {
		body = "#!/bin/sh\nset -eu\n" + body
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "FZF_DEFAULT_OPTS=blocked", "SHELL_PICKER_TEST=blocked"}
}

func newObservedCache(t testing.TB, body string, timeout time.Duration, observer func(process.ProcessEvent)) (*ZoxideCache, *processCounts) {
	t.Helper()
	name, environment := zoxideExecutable(t, body)
	counts := new(processCounts)
	runner := process.Runner{Observe: func(event process.ProcessEvent) {
		counts.observe(event)
		if observer != nil {
			observer(event)
		}
	}}
	cache, err := NewZoxideCache(runner, name, environment, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return cache, counts
}

func TestParseZoxidePolicyExact(t *testing.T) {
	for input, want := range map[string]ZoxidePolicy{"cached": ZoxideCached, "fresh": ZoxideFresh} {
		got, err := ParseZoxidePolicy(input)
		if err != nil || got != want {
			t.Fatalf("ParseZoxidePolicy(%q)=(%v,%v), want (%v,nil)", input, got, err, want)
		}
	}
	for _, input := range []string{"", "Cached", "FRESH", "other", " cached"} {
		if _, err := ParseZoxidePolicy(input); err == nil {
			t.Fatalf("ParseZoxidePolicy(%q) succeeded", input)
		}
	}
}

func TestNewZoxideCacheValidationAndDefaults(t *testing.T) {
	if _, err := NewZoxideCache(process.Runner{}, "zoxide", nil, -1); err == nil {
		t.Fatal("negative timeout succeeded")
	}
	want := 75 * time.Millisecond
	if runtime.GOOS == "windows" {
		want = 150 * time.Millisecond
	}
	if got := DefaultZoxideTimeout(); got != want {
		t.Fatalf("DefaultZoxideTimeout()=%v, want %v", got, want)
	}
}

func TestZoxideLoadParsesExactLFRecordsAndClonesResults(t *testing.T) {
	cache, counts := newObservedCache(t, "printf '/z/one\\n/z/two\\n'\n", time.Second, nil)
	if _, _, err := cache.Records(); err == nil {
		t.Fatal("Records succeeded before Load")
	}
	if err := cache.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, metrics, err := cache.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := [][]byte{records[0].Path, records[1].Path}; !reflect.DeepEqual(got, [][]byte{[]byte("/z/one"), []byte("/z/two")}) {
		t.Fatalf("paths=%q", got)
	}
	if records[0].Kind != protocol.KindZoxide || records[0].Target.Kind == 0 || metrics.ZoxideOutcome != "ok" {
		t.Fatalf("records=%+v metrics=%+v", records, metrics)
	}
	records[0].Path[0] = 'X'
	again, _, _ := cache.Records()
	if bytes.Equal(records[0].Path, again[0].Path) {
		t.Fatal("Records exposed mutable cached path")
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}

func TestZoxideEmptyOutputAndSanitizedEnvironment(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		cache, _ := newObservedCache(t, ":\n", time.Second, nil)
		if err := cache.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		records, metrics, err := cache.Records()
		if err != nil || len(records) != 0 || metrics.ZoxideOutcome != "ok" {
			t.Fatalf("records=%+v metrics=%+v err=%v", records, metrics, err)
		}
	})
	t.Run("sanitized", func(t *testing.T) {
		body := "test -z \"${FZF_DEFAULT_OPTS+x}\"\ntest -z \"${SHELL_PICKER_TEST+x}\"\nprintf '/z/env\\n'\n"
		cache, _ := newObservedCache(t, body, time.Second, nil)
		if err := cache.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		records, _, _ := cache.Records()
		if len(records) != 1 || string(records[0].Path) != "/z/env" {
			t.Fatalf("records=%+v", records)
		}
	})
}

func TestZoxidePreservesArbitraryPathBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX byte-oriented path fixture")
	}
	cache, _ := newObservedCache(t, "printf '/z/\\377\\n'\n", time.Second, nil)
	if err := cache.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, _, _ := cache.Records()
	want := []byte{'/', 'z', '/', 0xff}
	if len(records) != 1 || !bytes.Equal(records[0].Path, want) || !bytes.Equal(records[0].Target.Path, want) {
		t.Fatalf("records=%+v", records)
	}
}

func TestZoxideMalformedAndProcessFailuresDiscardCompleteBuffer(t *testing.T) {
	tests := []struct {
		name, body, outcome string
	}{
		{"leading-empty", "printf '\\n/z/one\\n'\n", "malformed"},
		{"interior-empty", "printf '/z/one\\n\\n/z/two\\n'\n", "malformed"},
		{"nul", "printf '/z/one\\000bad\\n'\n", "malformed"},
		{"relative", "printf 'relative\\n'\n", "malformed"},
		{"nonzero-partial", "printf '/z/partial\\n'\nexit 7\n", "process-error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache, _ := newObservedCache(t, test.body, time.Second, nil)
			if err := cache.Load(context.Background()); err == nil {
				t.Fatal("Load succeeded")
			}
			records, metrics, err := cache.Records()
			if err == nil || len(records) != 0 || metrics.ZoxideOutcome != test.outcome {
				t.Fatalf("records=%+v metrics=%+v err=%v", records, metrics, err)
			}
		})
	}
}

func TestZoxideMissingAndSpawnFailureAttemptWithoutStart(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		counts := new(processCounts)
		cache, err := NewZoxideCache(process.Runner{Observe: counts.observe}, filepath.Join(t.TempDir(), "absent-zoxide"), nil, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := cache.Load(context.Background()); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("err=%v", err)
		}
		_, metrics, _ := cache.Records()
		if attempts, starts, maxLive, _ := counts.values(); attempts != 1 || starts != 0 || maxLive != 0 || metrics.ZoxideOutcome != "missing" {
			t.Fatalf("counts=(%d,%d,%d) metrics=%+v", attempts, starts, maxLive, metrics)
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("spawn", func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "bad-zoxide"), []byte("not executable format"), 0o700); err != nil {
				t.Fatal(err)
			}
			counts := new(processCounts)
			cache, _ := NewZoxideCache(process.Runner{Observe: counts.observe}, filepath.Join(dir, "bad-zoxide"), nil, time.Second)
			if err := cache.Load(context.Background()); err == nil {
				t.Fatal("Load succeeded")
			}
			if attempts, starts, maxLive, _ := counts.values(); attempts != 1 || starts != 0 || maxLive != 0 {
				t.Fatalf("counts=(%d,%d,%d)", attempts, starts, maxLive)
			}
		})
	}
}

func TestZoxideTimeoutAndCallerCancellationDiscardPartialOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	t.Run("timeout", func(t *testing.T) {
		cache, counts := newObservedCache(t, "printf '/z/partial\\n'\nsleep 10\n", 20*time.Millisecond, nil)
		if err := cache.Load(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Load err=%v", err)
		}
		records, metrics, _ := cache.Records()
		if len(records) != 0 || metrics.ZoxideOutcome != "timeout" {
			t.Fatalf("records=%+v metrics=%+v", records, metrics)
		}
		if _, starts, _, exits := counts.values(); starts != 1 || exits != 1 {
			t.Fatalf("starts=%d exits=%d", starts, exits)
		}
	})
	t.Run("caller", func(t *testing.T) {
		started := make(chan struct{})
		var once sync.Once
		cache, _ := newObservedCache(t, "printf '/z/partial\\n'\nsleep 10\n", 0, func(event process.ProcessEvent) {
			if event.Phase == "start" {
				once.Do(func() { close(started) })
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- cache.Load(ctx) }()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		records, metrics, _ := cache.Records()
		if len(records) != 0 || metrics.ZoxideOutcome != "cancelled" {
			t.Fatalf("records=%+v metrics=%+v", records, metrics)
		}
	})
}

func TestZoxideConcurrentLoadAttemptsOnceAndPreservesObserver(t *testing.T) {
	var observed sync.Mutex
	callerEvents := 0
	cache, counts := newObservedCache(t, "printf '/z/one\\n'\n", 0, func(process.ProcessEvent) {
		observed.Lock()
		callerEvents++
		observed.Unlock()
	})
	const calls = 20
	var wait sync.WaitGroup
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := cache.Load(context.Background()); err != nil {
				t.Errorf("Load: %v", err)
			}
		}()
	}
	wait.Wait()
	if attempts, starts, _, exits := counts.values(); attempts != 1 || starts != 1 || exits != 1 {
		t.Fatalf("counts=(%d,%d,%d)", attempts, starts, exits)
	}
	observed.Lock()
	defer observed.Unlock()
	if callerEvents != 3 {
		t.Fatalf("caller observer events=%d", callerEvents)
	}
}

func zoxideRowsScript(count int) string {
	var script strings.Builder
	script.WriteString("printf '")
	for index := range count {
		fmt.Fprintf(&script, "/z/%d\\n", index)
	}
	script.WriteString("'\n")
	return script.String()
}

func benchmarkCache(b *testing.B, runner process.Runner, path string, environment []string, timeout time.Duration) *ZoxideCache {
	b.Helper()
	cache, err := NewZoxideCache(runner, path, environment, timeout)
	if err != nil {
		b.Fatal(err)
	}
	return cache
}

func assertProcessCounts(b *testing.B, counts *processCounts, attempts, starts, maxLive, exits int) {
	b.Helper()
	gotAttempts, gotStarts, gotMaxLive, gotExits := counts.values()
	if gotAttempts != attempts || gotStarts != starts || gotMaxLive != maxLive || gotExits != exits || counts.liveCount() != 0 {
		b.Fatalf("counts=(attempts=%d starts=%d max-live=%d exits=%d live=%d), want (%d,%d,%d,%d,0)",
			gotAttempts, gotStarts, gotMaxLive, gotExits, counts.liveCount(), attempts, starts, maxLive, exits)
	}
}

func BenchmarkInitialZoxideOverlap(b *testing.B) {
	path, environment := zoxideExecutable(b, zoxideRowsScript(10_000))
	counts := new(processCounts)
	runner := process.Runner{Observe: counts.observe}
	for range b.N {
		builder := &Builder{Cache: benchmarkCache(b, runner, path, environment, 0), Policy: ZoxideCached, enumerate: testLocal}
		if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); err != nil {
			b.Fatal(err)
		}
	}
	assertProcessCounts(b, counts, b.N, b.N, 1, b.N)
}

func BenchmarkZoxideTimeoutDiscard(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("POSIX shell timing fixture")
	}
	path, environment := zoxideExecutable(b, "printf '/z/partial\\n'\nsleep 10\n")
	for range b.N {
		counts := new(processCounts)
		cache := benchmarkCache(b, process.Runner{Observe: counts.observe}, path, environment, time.Millisecond)
		got, err := (&Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}).Build(context.Background(), testRequest(protocol.PickerCD, true))
		if err != nil || !got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "timeout" {
			b.Fatalf("got=%+v err=%v", got, err)
		}
		assertProcessCounts(b, counts, 1, 1, 1, 1)
	}
}

func BenchmarkCachedZoxideNavigation(b *testing.B) {
	path, environment := zoxideExecutable(b, zoxideRowsScript(10_000))
	counts := new(processCounts)
	runner := process.Runner{Observe: counts.observe}
	builder := &Builder{Cache: benchmarkCache(b, runner, path, environment, 0), Policy: ZoxideCached, enumerate: testLocal}
	if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); err != nil {
		b.Fatal(err)
	}
	attemptsBefore, _, _, _ := counts.values()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false)); err != nil {
			b.Fatal(err)
		}
	}
	assertProcessCounts(b, counts, attemptsBefore, 1, 1, 1)
}

func BenchmarkFreshZoxideNavigation(b *testing.B) {
	path, environment := zoxideExecutable(b, zoxideRowsScript(10_000))
	counts := new(processCounts)
	runner := process.Runner{Observe: counts.observe}
	builder := &Builder{Policy: ZoxideFresh, enumerate: testLocal, NewCache: func() (*ZoxideCache, error) {
		return NewZoxideCache(runner, path, environment, 0)
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false)); err != nil {
			b.Fatal(err)
		}
	}
	assertProcessCounts(b, counts, b.N, b.N, 1, b.N)
}

func BenchmarkCPZoxideProcessCountsStayZero(b *testing.B) {
	path, environment := zoxideExecutable(b, "printf '/z/one\\n'\n")
	for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
		b.Run(policy.String(), func(b *testing.B) {
			counts := new(processCounts)
			runner := process.Runner{Observe: counts.observe}
			builder := &Builder{Policy: policy, enumerate: testLocal}
			if policy == ZoxideCached {
				builder.Cache = benchmarkCache(b, runner, path, environment, 0)
			} else {
				builder.NewCache = func() (*ZoxideCache, error) { return NewZoxideCache(runner, path, environment, 0) }
			}
			for range b.N {
				if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCP, false)); err != nil {
					b.Fatal(err)
				}
			}
			assertProcessCounts(b, counts, 0, 0, 0, 0)
		})
	}
}
