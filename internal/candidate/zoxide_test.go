package candidate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type processCounts struct {
	mu                                     sync.Mutex
	attempts, starts, live, maxLive, exits int
}

const zoxideFixtureTimeout = 10 * time.Second

type observedErrContext struct {
	context.Context
	firstErr  func()
	secondErr func()
	calls     atomic.Int32
}

func (ctx *observedErrContext) Err() error {
	switch ctx.calls.Add(1) {
	case 1:
		ctx.firstErr()
	case 2:
		ctx.secondErr()
	}
	return ctx.Context.Err()
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

func newPortableObservedCache(t testing.TB, mode string, timeout time.Duration, observer func(process.ProcessEvent)) (*ZoxideCache, *processCounts) {
	t.Helper()
	counts := new(processCounts)
	runner := process.Runner{Observe: func(event process.ProcessEvent) {
		counts.observe(event)
		if observer != nil {
			observer(event)
		}
	}}
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), portableZoxideHelperEnv+"=1", portableZoxideModeEnv+"="+mode)
	cache, err := NewZoxideCache(runner, path, environment, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return cache, counts
}

func controlledZoxideCache(t testing.TB, loadErr error) *ZoxideCache {
	t.Helper()
	cache, err := NewZoxideCache(process.Runner{BeforeStart: func(process.Spec) error {
		return loadErr
	}}, "controlled-zoxide", nil, zoxideFixtureTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return cache
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
	cache, counts := newObservedCache(t, "printf '/z/one\\n/z/two\\n'\n", zoxideFixtureTimeout, nil)
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
	if got := [][]byte{records[0].Path, records[1].Path}; !reflect.DeepEqual(got, [][]byte{[]byte(portableZoxidePath("/z/one")), []byte(portableZoxidePath("/z/two"))}) {
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
		cache, _ := newObservedCache(t, ":\n", zoxideFixtureTimeout, nil)
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
		cache, _ := newObservedCache(t, body, zoxideFixtureTimeout, nil)
		if err := cache.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		records, _, _ := cache.Records()
		if len(records) != 1 || string(records[0].Path) != portableZoxidePath("/z/env") {
			t.Fatalf("records=%+v", records)
		}
	})
}

func TestZoxidePreservesArbitraryPathBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX byte-oriented path fixture")
	}
	cache, _ := newObservedCache(t, "printf '/z/\\377\\n'\n", zoxideFixtureTimeout, nil)
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
			cache, _ := newObservedCache(t, test.body, zoxideFixtureTimeout, nil)
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

func TestZoxideOutputLimiterAcceptsExactLimitsAndRejectsLimitPlusOne(t *testing.T) {
	tests := []struct {
		name                  string
		maxBytes, maxRows     int
		maxRowBytes           int
		first, second         []byte
		wantFirst, wantSecond int
	}{
		{name: "total bytes", maxBytes: 4, maxRows: 10, maxRowBytes: 10,
			first: []byte("abcd"), second: []byte("e"), wantFirst: 4, wantSecond: 0},
		{name: "row bytes", maxBytes: 100, maxRows: 10, maxRowBytes: 4,
			first: []byte("abcd"), second: []byte("e"), wantFirst: 4, wantSecond: 0},
		{name: "row count", maxBytes: 100, maxRows: 2, maxRowBytes: 10,
			first: []byte("a\nb\n"), second: []byte("c"), wantFirst: 4, wantSecond: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := newZoxideLimitWriter(&output, test.maxBytes, test.maxRows, test.maxRowBytes, nil)
			first, err := writer.Write(test.first)
			if err != nil || first != test.wantFirst {
				t.Fatalf("first write n=%d err=%v", first, err)
			}
			second, err := writer.Write(test.second)
			if !errors.Is(err, errZoxideOutputLimit) || second != test.wantSecond {
				t.Fatalf("second write n=%d err=%v", second, err)
			}
		})
	}
}

func TestZoxideOutputLimiterAcceptsExactDocumentedTotalAndRowCount(t *testing.T) {
	t.Run("total bytes", func(t *testing.T) {
		const row = "123456789\n"
		data := bytes.Repeat([]byte(row), MaxZoxideOutputBytes/len(row))
		data = append(data, bytes.Repeat([]byte{'x'}, MaxZoxideOutputBytes-len(data))...)
		writer := newZoxideLimitWriter(nil, MaxZoxideOutputBytes, MaxZoxideRows, MaxZoxideRowBytes, nil)
		if written, err := writer.Write(data); err != nil || written != len(data) {
			t.Fatalf("exact total write n=%d err=%v", written, err)
		}
		if err := writer.finalize(); err != nil {
			t.Fatalf("exact total finalize: %v", err)
		}
		if _, err := writer.Write([]byte{'x'}); !errors.Is(err, errZoxideOutputLimit) {
			t.Fatalf("total limit plus one err=%v", err)
		}
	})

	t.Run("row count", func(t *testing.T) {
		data := bytes.Repeat([]byte("x\n"), MaxZoxideRows)
		writer := newZoxideLimitWriter(nil, MaxZoxideOutputBytes, MaxZoxideRows, MaxZoxideRowBytes, nil)
		if written, err := writer.Write(data); err != nil || written != len(data) {
			t.Fatalf("exact row count write n=%d err=%v", written, err)
		}
		if err := writer.finalize(); err != nil {
			t.Fatalf("exact row count finalize: %v", err)
		}
		if _, err := writer.Write([]byte{'x'}); !errors.Is(err, errZoxideOutputLimit) {
			t.Fatalf("row count limit plus one err=%v", err)
		}
	})
}

func TestParseZoxideRecordsUsesTheDocumentedRowLimit(t *testing.T) {
	prefix := "/"
	if runtime.GOOS == "windows" {
		prefix = `C:\`
	}
	exact := []byte(prefix + strings.Repeat("x", MaxZoxideRowBytes-len(prefix)) + "\n")
	if _, err := parseZoxideRecords(exact); err != nil {
		t.Fatalf("exact row limit rejected: %v", err)
	}
	tooLong := []byte(prefix + strings.Repeat("x", MaxZoxideRowBytes-len(prefix)+1) + "\n")
	if _, err := parseZoxideRecords(tooLong); !errors.Is(err, errZoxideOutputLimit) {
		t.Fatalf("row limit plus one err=%v", err)
	}
}

func TestZoxideOutputLimitCancelsUnlimitedLoadAndBalancesProcessCounters(t *testing.T) {
	for _, mode := range []string{"over-total", "over-row", "over-rows"} {
		t.Run(mode, func(t *testing.T) {
			cache, counts := newPortableObservedCache(t, mode, 0, nil)
			if err := cache.Load(context.Background()); err == nil {
				t.Fatal("over-limit load succeeded")
			}
			records, metrics, err := cache.Records()
			if err == nil || len(records) != 0 || metrics.ZoxideOutcome != "malformed" {
				t.Fatalf("records=%+v metrics=%+v err=%v", records, metrics, err)
			}
			if attempts, starts, maxLive, exits := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 || counts.liveCount() != 0 {
				t.Fatalf("counts=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, starts, maxLive, exits, counts.liveCount())
			}
		})
	}
}

func TestZoxideMissingAndSpawnFailureAttemptWithoutStart(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		counts := new(processCounts)
		cache, err := NewZoxideCache(process.Runner{Observe: counts.observe}, filepath.Join(t.TempDir(), "absent-zoxide"), nil, zoxideFixtureTimeout)
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
			cache, _ := NewZoxideCache(process.Runner{Observe: counts.observe}, filepath.Join(dir, "bad-zoxide"), nil, zoxideFixtureTimeout)
			if err := cache.Load(context.Background()); err == nil {
				t.Fatal("Load succeeded")
			}
			if attempts, starts, maxLive, _ := counts.values(); attempts != 1 || starts != 0 || maxLive != 0 {
				t.Fatalf("counts=(%d,%d,%d)", attempts, starts, maxLive)
			}
		})
	}
}

func TestLoadInitialZoxideTurnsSoftSourceFailuresIntoResults(t *testing.T) {
	tests := []struct {
		name, mode, outcome string
		timeout             time.Duration
		makeCache           func(t *testing.T, mode string, timeout time.Duration) *ZoxideCache
	}{
		{name: "missing", outcome: "missing", makeCache: func(t *testing.T, _ string, _ time.Duration) *ZoxideCache {
			return controlledZoxideCache(t, exec.ErrNotFound)
		}},
		{name: "spawn-failure", outcome: "process-error", makeCache: func(t *testing.T, _ string, _ time.Duration) *ZoxideCache {
			return controlledZoxideCache(t, errors.New("controlled spawn failure"))
		}},
		{name: "malformed", mode: "malformed", timeout: zoxideFixtureTimeout, outcome: "malformed", makeCache: func(t *testing.T, mode string, timeout time.Duration) *ZoxideCache {
			cache, _ := newPortableObservedCache(t, mode, timeout, nil)
			return cache
		}},
		{name: "timeout", mode: "block", timeout: 20 * time.Millisecond, outcome: "timeout", makeCache: func(t *testing.T, mode string, timeout time.Duration) *ZoxideCache {
			cache, _ := newPortableObservedCache(t, mode, timeout, nil)
			return cache
		}},
		{name: "nonzero", mode: "nonzero", timeout: zoxideFixtureTimeout, outcome: "process-error", makeCache: func(t *testing.T, mode string, timeout time.Duration) *ZoxideCache {
			cache, _ := newPortableObservedCache(t, mode, timeout, nil)
			return cache
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder := &Builder{Cache: test.makeCache(t, test.mode, test.timeout), Policy: ZoxideCached}
			got, err := builder.LoadInitialZoxide(context.Background())
			if err != nil || !got.Discarded || len(got.Records) != 0 || got.Metrics.ZoxideOutcome != test.outcome {
				t.Fatalf("result=%+v err=%v, want discarded %q", got, err, test.outcome)
			}
		})
	}
}
