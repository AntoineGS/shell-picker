package candidate

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type causeCheckedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (ctx *causeCheckedContext) Err() error {
	ctx.once.Do(func() { close(ctx.checked) })
	return ctx.Context.Err()
}

func testRequest(picker protocol.Picker, initial bool) BuildRequest {
	return BuildRequest{Picker: picker, Location: pathutil.Filesystem([]byte("/local")), Initial: initial}
}

func TestBuilderIgnoresGenerationSemantically(t *testing.T) {
	cache, _ := newObservedCache(t, "printf '/z/one\\n'\n", time.Second, nil)
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	first := testRequest(protocol.PickerCP, false)
	first.Generation = 7
	second := first
	second.Generation = 99
	left, err := builder.Build(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := builder.Build(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.Records, right.Records) || left.Metrics.ZoxideOutcome != right.Metrics.ZoxideOutcome {
		t.Fatalf("generation changed builder result: left=%+v right=%+v", left, right)
	}
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
	if got.Metrics.ZoxideAttempts != 1 || got.Metrics.ZoxideStarts != 1 || got.Metrics.ZoxideExits != 1 ||
		got.Metrics.ZoxideProcesses != 1 || got.Metrics.ZoxideLive != 0 || got.Metrics.ZoxideMaxLive != 1 {
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
		if got.Metrics.ZoxideOutcome != "cached" || got.Metrics.ZoxideAttempts != 0 || got.Metrics.ZoxideStarts != 0 ||
			got.Metrics.ZoxideExits != 0 || got.Metrics.ZoxideProcesses != 0 || got.Metrics.ZoxideLive != 0 {
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

func TestFreshPolicyRejectsIncompleteConfiguration(t *testing.T) {
	builder := &Builder{Policy: ZoxideFresh, NewCache: func() (*ZoxideCache, error) {
		t.Fatal("incomplete fresh configuration invoked factory")
		return nil, nil
	}, enumerate: testLocal}
	if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); err == nil {
		t.Fatal("incomplete fresh configuration succeeded")
	}
}

func TestExplicitPolicyConfigurationClearsStaleStateAndSuppliesOneFreshPermit(t *testing.T) {
	stale, _ := newObservedCache(t, "printf '/stale\\n'\n", time.Second, nil)
	builder := &Builder{Cache: stale, Policy: ZoxideCached, NewCache: func() (*ZoxideCache, error) { return stale, nil }}
	factory := func() (*ZoxideCache, error) { return stale, nil }
	builder.ConfigureFresh(factory)
	if builder.Policy != ZoxideFresh || builder.Cache != nil || builder.NewCache == nil ||
		builder.freshPermit == nil || cap(builder.freshPermit) != 1 || len(builder.freshPermit) != 1 {
		t.Fatalf("fresh builder=%+v permit=(%d,%d)", builder, cap(builder.freshPermit), len(builder.freshPermit))
	}
	builder.ConfigureCached(stale)
	if builder.Policy != ZoxideCached || builder.Cache != stale || builder.NewCache != nil || builder.freshPermit != nil {
		t.Fatalf("cached builder=%+v", builder)
	}
}

func TestFreshPolicyAttemptsEveryCDGeneration(t *testing.T) {
	name, environment := zoxideExecutable(t, "printf '/z/one\\n'\n")
	counts := new(processCounts)
	builder := &Builder{enumerate: testLocal}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		return NewZoxideCache(process.Runner{Observe: counts.observe}, name, environment, time.Second)
	})
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
	builder := &Builder{enumerate: testLocal}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		return NewZoxideCache(process.Runner{Observe: counts.observe}, name, environment, 0)
	})
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

func TestFreshBuilderSerializesSessionQueriesAndCancelledWaiterDoesNotAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	name, environment := zoxideExecutable(t, "printf '/z/one\\n'\n")
	counts := new(processCounts)
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	runner := process.Runner{Observe: func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
			<-release
		}
	}}
	var factoryCalls atomic.Int32
	builder := &Builder{enumerate: testLocal}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		factoryCalls.Add(1)
		return NewZoxideCache(runner, name, environment, 0)
	})
	firstDone := make(chan error, 1)
	go func() {
		_, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
		firstDone <- err
	}()
	<-started

	cause := errors.New("cancelled behind permit")
	cancellable, cancelWaiter := context.WithCancelCause(context.Background())
	waiterCtx := &causeCheckedContext{Context: cancellable, checked: make(chan struct{})}
	waiterDone := make(chan error, 1)
	go func() {
		_, err := builder.Build(waiterCtx, testRequest(protocol.PickerCD, false))
		waiterDone <- err
	}()
	<-waiterCtx.checked
	cancelWaiter(cause)
	select {
	case err := <-waiterDone:
		if !errors.Is(err, cause) {
			t.Fatalf("waiter err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		release <- struct{}{}
		<-firstDone
		<-waiterDone
		t.Fatal("cancelled waiter remained blocked behind fresh generation")
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("factory calls=%d", factoryCalls.Load())
	}
	if attempts, starts, maxLive, _ := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 {
		t.Fatalf("counts=(%d,%d,%d)", attempts, starts, maxLive)
	}

	release <- struct{}{}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	nextDone := make(chan error, 1)
	go func() {
		_, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
		nextDone <- err
	}()
	<-started
	release <- struct{}{}
	if err := <-nextDone; err != nil {
		t.Fatal(err)
	}
	if attempts, starts, maxLive, exits := counts.values(); factoryCalls.Load() != 2 || attempts != 2 || starts != 2 || maxLive != 1 || exits != 2 {
		t.Fatalf("factory=%d counts=(%d,%d,%d,%d)", factoryCalls.Load(), attempts, starts, maxLive, exits)
	}
}

func TestIndependentFreshSessionBuildersMayQueryConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	name, environment := zoxideExecutable(t, "printf '/z/one\\n'\n")
	counts := new(processCounts)
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	runner := process.Runner{Observe: func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
			<-release
		}
	}}
	newBuilder := func() *Builder {
		builder := &Builder{enumerate: testLocal}
		builder.ConfigureFresh(func() (*ZoxideCache, error) {
			return NewZoxideCache(runner, name, environment, 0)
		})
		return builder
	}
	done := make(chan error, 2)
	for range 2 {
		builder := newBuilder()
		go func() {
			_, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("independent session did not start concurrently")
		}
	}
	if _, starts, maxLive, _ := counts.values(); starts != 2 || maxLive != 2 {
		t.Fatalf("starts=%d max-live=%d", starts, maxLive)
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 2 || starts != 2 || maxLive != 2 || exits != 2 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}

func TestCPNeverLoadsCacheOrInvokesFreshFactory(t *testing.T) {
	for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
		t.Run(policy.String(), func(t *testing.T) {
			cache, counts := newObservedCache(t, "printf '/z/one\\n'\n", time.Second, nil)
			builder := &Builder{Cache: cache, Policy: policy, enumerate: testLocal}
			if policy == ZoxideFresh {
				builder.ConfigureFresh(func() (*ZoxideCache, error) {
					t.Fatal("fresh factory called for cp")
					return nil, nil
				})
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

func TestCallerCancellationBeforePrivateTimeoutWinsAndReaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell timing fixture")
	}
	started := make(chan struct{})
	var once sync.Once
	cache, counts := newObservedCache(t, "printf '/z/partial\\n'\nsleep 10\n", 250*time.Millisecond, func(event process.ProcessEvent) {
		if event.Phase == "start" {
			once.Do(func() { close(started) })
		}
	})
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	cause := errors.New("caller cancelled first")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := builder.Build(ctx, testRequest(protocol.PickerCD, true))
		done <- struct {
			result BuildResult
			err    error
		}{result, err}
	}()
	<-started
	cancel(cause)
	got := <-done
	if !errors.Is(got.err, cause) || len(got.result.Records) != 0 {
		t.Fatalf("result=%+v err=%v", got.result, got.err)
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
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
	exitObserved := make(chan struct{})
	releaseExit := make(chan struct{})
	var exitOnce sync.Once
	cache, counts := newObservedCache(t, "printf '/z/partial\\n'\nsleep 10\n", 20*time.Millisecond, func(event process.ProcessEvent) {
		if event.Phase == "exit" {
			exitOnce.Do(func() { close(exitObserved) })
			<-releaseExit
		}
	})
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := builder.Build(ctx, testRequest(protocol.PickerCD, true))
		done <- struct {
			result BuildResult
			err    error
		}{result, err}
	}()
	<-exitObserved
	cancel(errors.New("caller cancelled after private timeout"))
	close(releaseExit)
	completed := <-done
	got, err := completed.result, completed.err
	if err != nil || len(got.Records) != 2 || !got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "timeout" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}
