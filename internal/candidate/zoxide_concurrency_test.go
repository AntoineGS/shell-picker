package candidate

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestLoadInitialZoxideParentCancellationIsHard(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	cache, _ := newPortableObservedCache(t, "block", 0, func(event process.ProcessEvent) {
		if event.Phase == "start" {
			once.Do(func() { close(started) })
		}
	})
	builder := &Builder{Cache: cache, Policy: ZoxideCached}
	cause := errors.New("parent cancelled")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct {
		result InitialZoxideResult
		err    error
	}, 1)
	go func() {
		result, err := builder.LoadInitialZoxide(ctx)
		done <- struct {
			result InitialZoxideResult
			err    error
		}{result, err}
	}()
	<-started
	cancel(cause)
	got := <-done
	if !errors.Is(got.err, cause) || !got.result.Discarded || len(got.result.Records) != 0 ||
		got.result.Metrics.ZoxideOutcome != "cancelled" || got.result.Metrics.ZoxideAttempts != 1 ||
		got.result.Metrics.ZoxideStarts != 1 || got.result.Metrics.ZoxideExits != 1 ||
		got.result.Metrics.ZoxideProcesses != 1 || got.result.Metrics.ZoxideLive != 0 ||
		got.result.Metrics.ZoxideMaxLive != 1 {
		t.Fatalf("result=%+v err=%v", got.result, got.err)
	}
}

func TestLoadInitialZoxideCachedStateLoadsAtMostOnce(t *testing.T) {
	cache, counts := newPortableObservedCache(t, "ok", zoxideFixtureTimeout, nil)
	builder := &Builder{Cache: cache, Policy: ZoxideCached}
	for range 2 {
		got, err := builder.LoadInitialZoxide(context.Background())
		if err != nil || got.Discarded || len(got.Records) != 1 || got.Metrics.ZoxideOutcome != "ok" {
			t.Fatalf("result=%+v err=%v", got, err)
		}
	}
	if attempts, starts, maxLive, exits := counts.values(); attempts != 1 || starts != 1 || maxLive != 1 || exits != 1 {
		t.Fatalf("counts=(%d,%d,%d,%d)", attempts, starts, maxLive, exits)
	}
}

func TestBuilderLoadInitialZoxideWaiterCancellationPreservesCauseAfterSharedTimeout(t *testing.T) {
	exitObserved := make(chan struct{})
	releaseExit := make(chan struct{})
	var exitOnce, releaseOnce sync.Once
	cache, _ := newPortableObservedCache(t, "block", 20*time.Millisecond, func(event process.ProcessEvent) {
		if event.Phase == "exit" {
			exitOnce.Do(func() {
				close(exitObserved)
				<-releaseExit
			})
		}
	})
	release := func() { releaseOnce.Do(func() { close(releaseExit) }) }
	defer release()
	builder := &Builder{Cache: cache, Policy: ZoxideCached}

	ownerDone := make(chan struct {
		result InitialZoxideResult
		err    error
	}, 1)
	go func() {
		result, err := builder.LoadInitialZoxide(context.Background())
		ownerDone <- struct {
			result InitialZoxideResult
			err    error
		}{result, err}
	}()
	<-exitObserved

	baseContext, cancel := context.WithCancelCause(context.Background())
	validated := make(chan struct{})
	var validatedOnce sync.Once
	waiterContext := &observedErrContext{
		Context: baseContext,
		firstErr: func() {
			validatedOnce.Do(func() { close(validated) })
		},
		secondErr: func() {
			release()
			<-cache.ready
		},
	}
	waiterDone := make(chan struct {
		result InitialZoxideResult
		err    error
	}, 1)
	go func() {
		result, err := builder.LoadInitialZoxide(waiterContext)
		waiterDone <- struct {
			result InitialZoxideResult
			err    error
		}{result, err}
	}()
	<-validated
	cause := errors.New("waiter cancelled during shared timeout")
	cancel(cause)
	waiter := <-waiterDone
	owner := <-ownerDone
	var waiterCancellation *zoxideWaiterCancellationError
	if !errors.Is(waiter.err, cause) || !errors.As(waiter.err, &waiterCancellation) || len(waiter.result.Records) != 0 ||
		waiter.result.Discarded || waiter.result.Metrics != (SourceMetrics{}) {
		t.Fatalf("waiter result=%+v err=%v, want caller cause", waiter.result, waiter.err)
	}
	if owner.err != nil || !owner.result.Discarded || owner.result.Metrics.ZoxideOutcome != "timeout" {
		t.Fatalf("owner result=%+v err=%v, want soft shared timeout", owner.result, owner.err)
	}
	_, metrics, err := cache.Records()
	if err == nil || metrics.ZoxideOutcome != "timeout" {
		t.Fatalf("terminal cache metrics=%+v err=%v, want timeout", metrics, err)
	}
}

func TestBuilderLoadInitialZoxideOwnerTimeoutStaysSoftAfterParentDeadline(t *testing.T) {
	exitObserved := make(chan struct{})
	releaseExit := make(chan struct{})
	var exitOnce, releaseOnce sync.Once
	cache, _ := newPortableObservedCache(t, "block", 20*time.Millisecond, func(event process.ProcessEvent) {
		if event.Phase == "exit" {
			exitOnce.Do(func() {
				close(exitObserved)
				<-releaseExit
			})
		}
	})
	release := func() { releaseOnce.Do(func() { close(releaseExit) }) }
	defer release()
	builder := &Builder{Cache: cache, Policy: ZoxideCached}
	parent, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct {
		result InitialZoxideResult
		err    error
	}, 1)
	go func() {
		result, err := builder.LoadInitialZoxide(parent)
		done <- struct {
			result InitialZoxideResult
			err    error
		}{result, err}
	}()
	<-exitObserved
	cancel(context.DeadlineExceeded)
	release()
	got := <-done
	if got.err != nil || !got.result.Discarded || got.result.Metrics.ZoxideOutcome != "timeout" {
		t.Fatalf("result=%+v err=%v, want soft private timeout", got.result, got.err)
	}
}

func TestZoxideConcurrentWaiterCancellationDoesNotWaitForUnboundedLoad(t *testing.T) {
	loadStarted := make(chan struct{})
	release := make(chan struct{})
	var loadCalls atomic.Int32
	loadErr := errors.New("underlying load finished")
	cache, err := NewZoxideCache(process.Runner{BeforeStart: func(process.Spec) error {
		loadCalls.Add(1)
		close(loadStarted)
		<-release
		return loadErr
	}}, "gated-zoxide", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- cache.Load(context.Background()) }()
	<-loadStarted

	cause := errors.New("waiter cancelled")
	waiterContext, cancelWaiter := context.WithCancelCause(context.Background())
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- cache.Load(waiterContext) }()
	cancelWaiter(cause)
	select {
	case err := <-waiterDone:
		if !errors.Is(err, cause) {
			t.Fatalf("waiter error=%v, want %v", err, cause)
		}
	case <-time.After(time.Second):
		close(release)
		<-firstDone
		t.Fatal("cancelled waiter remained blocked behind the first load")
	}

	if got := loadCalls.Load(); got != 1 {
		t.Fatalf("underlying load calls before release=%d, want 1", got)
	}
	close(release)
	if err := <-firstDone; !errors.Is(err, loadErr) {
		t.Fatalf("first load error=%v, want %v", err, loadErr)
	}
	if err := cache.Load(context.Background()); !errors.Is(err, loadErr) {
		t.Fatalf("cached load error=%v, want %v", err, loadErr)
	}
	if got := loadCalls.Load(); got != 1 {
		t.Fatalf("underlying load calls=%d, want 1", got)
	}
}

func TestZoxideConcurrentWaiterCancellationBeatsSharedTimeout(t *testing.T) {
	loadStarted := make(chan struct{})
	release := make(chan struct{})
	var loadCalls atomic.Int32
	cache, err := NewZoxideCache(process.Runner{BeforeStart: func(process.Spec) error {
		loadCalls.Add(1)
		close(loadStarted)
		<-release
		return errZoxideTimeout
	}}, "gated-zoxide", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- cache.Load(context.Background()) }()
	<-loadStarted

	cause := errors.New("parent cancelled before shared timeout")
	waiterContext, cancelWaiter := context.WithCancelCause(context.Background())
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- cache.Load(waiterContext) }()
	cancelWaiter(cause)
	select {
	case err := <-waiterDone:
		if !errors.Is(err, cause) {
			t.Fatalf("waiter error=%v, want %v", err, cause)
		}
	case <-time.After(time.Second):
		close(release)
		<-firstDone
		t.Fatal("cancelled waiter did not finish")
	}
	close(release)
	if err := <-firstDone; !errors.Is(err, errZoxideTimeout) {
		t.Fatalf("first load error=%v, want %v", err, errZoxideTimeout)
	}
	if err := cache.Load(context.Background()); !errors.Is(err, errZoxideTimeout) {
		t.Fatalf("cached load error=%v, want %v", err, errZoxideTimeout)
	}
	if got := loadCalls.Load(); got != 1 {
		t.Fatalf("underlying load calls=%d, want 1", got)
	}
	_, metrics, err := cache.Records()
	if err == nil || metrics.ZoxideOutcome != "timeout" {
		t.Fatalf("terminal cache metrics=%+v err=%v, want timeout", metrics, err)
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
	cache, counts := newObservedCache(t, "printf '/z/one\\n'\n", zoxideFixtureTimeout, func(process.ProcessEvent) {
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

func BenchmarkNavigationLocalOnly(b *testing.B) {
	path, environment := zoxideExecutable(b, zoxideRowsScript(10_000))
	for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
		b.Run(policy.String(), func(b *testing.B) {
			counts := new(processCounts)
			runner := process.Runner{Observe: counts.observe}
			builder := &Builder{enumerate: testLocal}
			if policy == ZoxideCached {
				builder.ConfigureCached(benchmarkCache(b, runner, path, environment, 0))
			} else {
				builder.ConfigureFresh(func() (*ZoxideCache, error) {
					return NewZoxideCache(runner, path, environment, 0)
				})
			}
			if _, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true)); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
				if err != nil {
					b.Fatal(err)
				}
				if got.Metrics.ZoxideOutcome != "not-run" || got.Metrics.ZoxideAttempts != 0 ||
					got.Metrics.ZoxideStarts != 0 || got.Metrics.ZoxideExits != 0 || got.Metrics.ZoxideProcesses != 0 ||
					got.Metrics.ZoxideLive != 0 || got.Metrics.ZoxideMaxLive != 0 {
					b.Fatalf("navigation metrics=%+v", got.Metrics)
				}
			}
			b.StopTimer()
			assertProcessCounts(b, counts, 1, 1, 1, 1)
		})
	}
}

func BenchmarkCPZoxideProcessCountsStayZero(b *testing.B) {
	path, environment := zoxideExecutable(b, "printf '/z/one\\n'\n")
	for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
		b.Run(policy.String(), func(b *testing.B) {
			counts := new(processCounts)
			runner := process.Runner{Observe: counts.observe}
			builder := &Builder{enumerate: testLocal}
			if policy == ZoxideCached {
				builder.ConfigureCached(benchmarkCache(b, runner, path, environment, 0))
			} else {
				builder.ConfigureFresh(func() (*ZoxideCache, error) { return NewZoxideCache(runner, path, environment, 0) })
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
