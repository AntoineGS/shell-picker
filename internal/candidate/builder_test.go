package candidate

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func testRequest(picker protocol.Picker, initial bool) BuildRequest {
	return BuildRequest{Picker: picker, Location: pathutil.Filesystem([]byte("/local")), Initial: initial}
}

func testLocal(_ context.Context, picker protocol.Picker, _ pathutil.Location, _ LocalOptions) ([]Record, error) {
	kind := protocol.KindLocal
	if picker == protocol.PickerCP {
		kind = protocol.KindDirectory
	}
	return []Record{newRecord(kind, ".", []byte("/local")), newRecord(kind, "same", []byte("/z/same"))}, nil
}

func paths(records []Record) []string {
	result := make([]string, len(records))
	for index := range records {
		result[index] = string(records[index].Path)
	}
	return result
}

func TestInitialBuilderOverlapsCacheLoadAndMergesLocalFirst(t *testing.T) {
	localStarted := make(chan struct{})
	zoxideStarted := make(chan struct{})
	var localOnce, zoxideOnce sync.Once
	cache, _ := newObservedCache(t, "printf '/z/same\\n/z/one\\n/z/two\\n'\n", time.Second, func(event process.ProcessEvent) {
		if event.Phase == "start" {
			zoxideOnce.Do(func() { close(zoxideStarted) })
			<-localStarted
		}
	})
	builder := &Builder{
		Cache:  cache,
		Policy: ZoxideCached,
		enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
			localOnce.Do(func() { close(localStarted) })
			<-zoxideStarted
			return testLocal(context.Background(), protocol.PickerCD, pathutil.Location{}, LocalOptions{})
		},
	}
	got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/local", "/z/same", "/z/one", "/z/two"}; !reflect.DeepEqual(paths(got.Records), want) {
		t.Fatalf("paths=%q, want %q", paths(got.Records), want)
	}
	if got.Metrics.ZoxideAttempts != 1 || got.Metrics.ZoxideStarts != 1 || got.Metrics.ZoxideMaxLive != 1 {
		t.Fatalf("metrics=%+v", got.Metrics)
	}
}

func TestCachedPolicyAttemptsOnceForSessionAndLaterReportsCached(t *testing.T) {
	cache, counts := newObservedCache(t, "printf '/z/one\\n'\n", time.Second, nil)
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	first, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
		if err != nil {
			t.Fatal(err)
		}
		if got.Metrics.ZoxideOutcome != "cached" || got.Metrics.ZoxideAttempts != 0 || got.Metrics.ZoxideStarts != 0 {
			t.Fatalf("later metrics=%+v", got.Metrics)
		}
	}
	if first.Metrics.ZoxideOutcome != "ok" {
		t.Fatalf("first metrics=%+v", first.Metrics)
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}

func TestFreshPolicyAttemptsEveryCDGeneration(t *testing.T) {
	name, environment := zoxideExecutable(t, "printf '/z/one\\n'\n")
	counts := new(processCounts)
	builder := &Builder{
		Policy:    ZoxideFresh,
		enumerate: testLocal,
		NewCache: func() (*ZoxideCache, error) {
			return NewZoxideCache(process.Runner{Observe: counts.observe}, name, environment, time.Second)
		},
	}
	for _, initial := range []bool{true, false} {
		got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, initial))
		if err != nil {
			t.Fatal(err)
		}
		if got.Metrics.ZoxideAttempts != 1 || got.Metrics.ZoxideStarts != 1 || got.Metrics.ZoxideMaxLive != 1 {
			t.Fatalf("metrics=%+v", got.Metrics)
		}
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 2 || starts != 2 || maxLive != 1 || exits != 2 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}

func TestFreshZeroTimeoutIsAuthoritativeUnlimitedPerGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	name, environment := zoxideExecutable(t, "sleep 0.1\nprintf '/z/one\\n'\n")
	counts := new(processCounts)
	builder := &Builder{Policy: ZoxideFresh, enumerate: testLocal, NewCache: func() (*ZoxideCache, error) {
		return NewZoxideCache(process.Runner{Observe: counts.observe}, name, environment, 0)
	}}
	for _, initial := range []bool{true, false} {
		got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, initial))
		if err != nil || !reflect.DeepEqual(paths(got.Records), []string{"/local", "/z/same", "/z/one"}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if attempts, starts, maxLive, _ := counts.values(); attempts != 2 || starts != 2 || maxLive != 1 {
		t.Fatalf("counts=(%d,%d,%d)", attempts, starts, maxLive)
	}
}

func TestFreshConcurrentBuildsKeepOneGlobalZoxideProcessLive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	name, environment := zoxideExecutable(t, "sleep 0.05\nprintf '/z/one\\n'\n")
	counts := new(processCounts)
	builder := &Builder{Policy: ZoxideFresh, enumerate: testLocal, NewCache: func() (*ZoxideCache, error) {
		return NewZoxideCache(process.Runner{Observe: counts.observe}, name, environment, 0)
	}}
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
			errorsFound <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 2 || starts != 2 || maxLive != 1 || exits != 2 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}

func TestCPNeverLoadsCacheOrInvokesFreshFactory(t *testing.T) {
	for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
		t.Run(policy.String(), func(t *testing.T) {
			cache, counts := newObservedCache(t, "printf '/z/one\\n'\n", time.Second, nil)
			builder := &Builder{Cache: cache, Policy: policy, enumerate: testLocal}
			if policy == ZoxideFresh {
				builder.Cache = nil
				builder.NewCache = func() (*ZoxideCache, error) {
					t.Fatal("fresh factory called for cp")
					return nil, nil
				}
			}
			got, err := builder.Build(context.Background(), testRequest(protocol.PickerCP, true))
			if err != nil {
				t.Fatal(err)
			}
			if got.Metrics.ZoxideOutcome != "not-run" || got.Metrics.ZoxideAttempts != 0 || got.Metrics.ZoxideStarts != 0 {
				t.Fatalf("metrics=%+v", got.Metrics)
			}
			if attempts, starts, _, _ := counts.values(); attempts != 0 || starts != 0 {
				t.Fatalf("counts=(%d,%d)", attempts, starts)
			}
		})
	}
}

func TestBuilderPreservesVirtualRecordAndDeduplicatesOnlyFilesystemTargets(t *testing.T) {
	cache, _ := newObservedCache(t, "printf '/z/same\\n/z/other\\n'\n", time.Second, nil)
	virtual := newVirtualDrivesRecord("..")
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
		return []Record{virtual, newRecord(protocol.KindLocal, "same", []byte("/z/same"))}, nil
	}}
	got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 3 || got.Records[0].Kind != protocol.KindVirtual || got.Records[0].FullKey() != virtual.FullKey() || got.Records[0].Target.Kind != pathutil.KindDrives {
		t.Fatalf("records=%+v", got.Records)
	}
}

func TestLocalHardErrorCancelsAndWaitsForZoxide(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	started := make(chan struct{})
	var once sync.Once
	cache, counts := newObservedCache(t, "sleep 10\n", 0, func(event process.ProcessEvent) {
		if event.Phase == "start" {
			once.Do(func() { close(started) })
		}
	})
	want := errors.New("local failed")
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
		<-started
		return nil, want
	}}
	if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	if _, starts, _, exits := counts.values(); starts != 1 || exits != 1 {
		t.Fatalf("starts=%d exits=%d", starts, exits)
	}
}

func TestBuilderCallerCancellationIsHard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	started := make(chan struct{})
	var once sync.Once
	cache, _ := newObservedCache(t, "sleep 10\n", time.Second, func(event process.ProcessEvent) {
		if event.Phase == "start" {
			once.Do(func() { close(started) })
		}
	})
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: func(ctx context.Context, _ protocol.Picker, _ pathutil.Location, _ LocalOptions) ([]Record, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := builder.Build(ctx, testRequest(protocol.PickerCD, true)); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuilderCancellationAfterLocalEnumerationPreservesCause(t *testing.T) {
	cache, _ := newObservedCache(t, "printf '/z/one\\n'\n", time.Second, nil)
	if err := cache.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := errors.New("session stopped")
	for _, request := range []BuildRequest{testRequest(protocol.PickerCP, false), testRequest(protocol.PickerCD, false)} {
		t.Run(string(request.Picker), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
				cancel(want)
				return testLocal(context.Background(), request.Picker, pathutil.Location{}, LocalOptions{})
			}}
			if _, err := builder.Build(ctx, request); !errors.Is(err, want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestBuilderZoxideTimeoutIsSoftAndDiscardsPartialOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	cache, _ := newObservedCache(t, "printf '/z/partial\\n'\nsleep 10\n", 20*time.Millisecond, nil)
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 || !got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "timeout" {
		t.Fatalf("got=%+v", got)
	}
}

func BenchmarkInitialZoxideOverlap(b *testing.B) {
	for range b.N {
		cache, _ := newObservedCache(b, "printf '/z/one\\n'\n", 0, nil)
		builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
		if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkZoxideTimeoutDiscard(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("POSIX shell timing fixture")
	}
	for range b.N {
		cache, _ := newObservedCache(b, "printf '/z/partial\\n'\nsleep 10\n", time.Millisecond, nil)
		builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
		got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
		if err != nil || !got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "timeout" {
			b.Fatalf("got=%+v err=%v", got, err)
		}
	}
}

func BenchmarkCachedZoxideNavigation(b *testing.B) {
	cache, _ := newObservedCache(b, "printf '/z/one\\n'\n", 0, nil)
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFreshZoxideNavigation(b *testing.B) {
	name, environment := zoxideExecutable(b, "printf '/z/one\\n'\n")
	builder := &Builder{Policy: ZoxideFresh, enumerate: testLocal, NewCache: func() (*ZoxideCache, error) {
		return NewZoxideCache(process.Runner{}, name, environment, 0)
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false)); err != nil {
			b.Fatal(err)
		}
	}
}
