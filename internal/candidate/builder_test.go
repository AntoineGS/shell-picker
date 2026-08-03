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

func TestBuildLocalCompletesWhileInitialZoxideIsBlocked(t *testing.T) {
	zoxideStarted := make(chan struct{})
	releaseZoxide := make(chan struct{})
	cache, err := NewZoxideCache(process.Runner{BeforeStart: func(process.Spec) error {
		close(zoxideStarted)
		<-releaseZoxide
		return errors.New("blocked zoxide")
	}}, "blocked-zoxide", nil, zoxideFixtureTimeout)
	if err != nil {
		t.Fatal(err)
	}
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}

	zoxideDone := make(chan struct {
		result InitialZoxideResult
		err    error
	}, 1)
	go func() {
		result, err := builder.LoadInitialZoxide(context.Background())
		zoxideDone <- struct {
			result InitialZoxideResult
			err    error
		}{result, err}
	}()
	<-zoxideStarted

	local, err := builder.BuildLocal(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil || !reflect.DeepEqual(paths(local.Records), []string{"/local", "/z/same"}) {
		t.Fatalf("local=%+v err=%v", local, err)
	}

	close(releaseZoxide)
	loaded := <-zoxideDone
	if loaded.err != nil || !loaded.result.Discarded || loaded.result.Metrics.ZoxideOutcome != "process-error" {
		t.Fatalf("zoxide=%+v err=%v", loaded.result, loaded.err)
	}
}

func TestMergeNewRecordsUsesMergeRecordsIdentityAndOrdering(t *testing.T) {
	baseVirtual := newVirtualDrivesRecord("..")
	base := []Record{
		newRecord(protocol.KindLocal, "base", []byte("/base")),
		baseVirtual,
	}
	additions := []Record{
		newRecord(protocol.KindZoxide, "duplicate-base", []byte("/base")),
		newRecord(protocol.KindZoxide, "new", []byte("/new")),
		newRecord(protocol.KindZoxide, "duplicate-new", []byte("/new")),
		newVirtualDrivesRecord(".."),
		newVirtualDrivesRecord("different-display"),
	}

	merged, admitted := MergeNewRecords(base, additions)
	if got, want := fullKeys(merged), []string{base[0].FullKey(), baseVirtual.FullKey(), additions[1].FullKey(), additions[4].FullKey()}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged keys=%q, want %q", got, want)
	}
	if got, want := fullKeys(admitted), []string{additions[1].FullKey(), additions[4].FullKey()}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admitted keys=%q, want %q", got, want)
	}
	if merged[0].FullKey() != base[0].FullKey() || merged[1].FullKey() != baseVirtual.FullKey() {
		t.Fatalf("base records were not preserved first: %+v", merged)
	}
}

func TestMergeNewRecordsUsesPlatformFilesystemIdentityWithoutChangingRecords(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filesystem aliases")
	}

	base := newRecord(protocol.KindLocal, `C:\Work\Base`, []byte(`C:\Work\Base\Item`))
	addition := newRecord(protocol.KindZoxide, `c:/work/base/item`, []byte(`c:/work/base/item`))
	merged, admitted := MergeNewRecords([]Record{base}, []Record{addition})

	if len(merged) != 1 || len(admitted) != 0 {
		t.Fatalf("merged=%+v admitted=%+v, want base alias deduplicated", merged, admitted)
	}
	if string(merged[0].Path) != `C:\Work\Base\Item` || merged[0].Display != base.Display || merged[0].Payload != base.Payload {
		t.Fatalf("base record changed: %+v", merged[0])
	}
}

func readyZoxideCache(records []Record, metrics SourceMetrics) *ZoxideCache {
	cache := &ZoxideCache{
		ready:   make(chan struct{}),
		records: cloneRecords(records),
		metrics: metrics,
	}
	cache.once.Do(func() {})
	close(cache.ready)
	return cache
}

func TestBuildLocalValidatesWithoutTouchingZoxide(t *testing.T) {
	var factoryCalls atomic.Int32
	builder := &Builder{enumerate: testLocal}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		factoryCalls.Add(1)
		t.Fatal("BuildLocal invoked the zoxide factory")
		return nil, nil
	})

	got, err := builder.BuildLocal(context.Background(), testRequest(protocol.PickerCP, true))
	if err != nil {
		t.Fatal(err)
	}
	if got.Metrics.ZoxideOutcome != "not-run" || got.Metrics.ZoxideAttempts != 0 || factoryCalls.Load() != 0 {
		t.Fatalf("local-only result=%+v factory-calls=%d", got, factoryCalls.Load())
	}
}

func TestBuilderMethodsRejectNilBuilder(t *testing.T) {
	var builder *Builder
	for name, call := range map[string]func() error{
		"local": func() error {
			_, err := builder.BuildLocal(context.Background(), testRequest(protocol.PickerCD, false))
			return err
		},
		"zoxide": func() error {
			_, err := builder.LoadInitialZoxide(context.Background())
			return err
		},
		"build": func() error {
			_, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("nil builder succeeded")
			}
		})
	}
}

func TestBuildLocalRejectsInvalidContextAndPickerBeforeEnumeration(t *testing.T) {
	cache := readyZoxideCache(nil, SourceMetrics{ZoxideOutcome: "ok"})
	var enumerateCalls atomic.Int32
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
		enumerateCalls.Add(1)
		return nil, nil
	}}
	cancelled, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("already cancelled")
	cancel(cause)

	tests := []struct {
		name    string
		ctx     context.Context
		request BuildRequest
	}{
		{name: "nil context", request: testRequest(protocol.PickerCD, false)},
		{name: "cancelled context", ctx: cancelled, request: testRequest(protocol.PickerCD, false)},
		{name: "unsupported picker", ctx: context.Background(), request: BuildRequest{Picker: protocol.Picker("unknown")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := builder.BuildLocal(test.ctx, test.request); err == nil {
				t.Fatal("invalid BuildLocal request succeeded")
			}
		})
	}
	if got := enumerateCalls.Load(); got != 0 {
		t.Fatalf("enumeration calls=%d, want 0", got)
	}
}

func TestLoadInitialZoxideUsesCachedStateAndReturnsClones(t *testing.T) {
	source := newRecord(protocol.KindZoxide, "zoxide", []byte("/zoxide"))
	cache := readyZoxideCache([]Record{source}, SourceMetrics{ZoxideOutcome: "ok", ZoxideAttempts: 1})
	builder := &Builder{Cache: cache, Policy: ZoxideCached}

	got, err := builder.LoadInitialZoxide(context.Background())
	if err != nil || got.Discarded || got.Metrics.ZoxideOutcome != "ok" || !reflect.DeepEqual(paths(got.Records), []string{"/zoxide"}) {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	got.Records[0].Path[0] = 'X'
	again, _, err := cache.Records()
	if err != nil || !reflect.DeepEqual(paths(again), []string{"/zoxide"}) {
		t.Fatalf("cache records changed through result: records=%+v err=%v", again, err)
	}
}

func TestLoadInitialZoxideWaitsForBlockingCacheLoad(t *testing.T) {
	loadStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	cache, err := NewZoxideCache(process.Runner{BeforeStart: func(process.Spec) error {
		close(loadStarted)
		<-release
		return errors.New("controlled cache load")
	}}, "blocked-zoxide", nil, zoxideFixtureTimeout)
	if err != nil {
		t.Fatal(err)
	}
	builder := &Builder{Cache: cache, Policy: ZoxideCached}
	releaseCache := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCache()
	done := make(chan struct {
		result InitialZoxideResult
		err    error
	}, 1)
	go func() {
		result, err := builder.LoadInitialZoxide(context.Background())
		done <- struct {
			result InitialZoxideResult
			err    error
		}{result, err}
	}()
	<-loadStarted
	select {
	case got := <-done:
		t.Fatalf("LoadInitialZoxide returned before cache release: result=%+v err=%v", got.result, got.err)
	default:
	}
	releaseCache()
	got := <-done
	if got.err != nil || !got.result.Discarded || got.result.Metrics.ZoxideOutcome != "process-error" {
		t.Fatalf("result=%+v err=%v", got.result, got.err)
	}
}

func TestLoadInitialZoxideFreshPermitCancellationAndReuse(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	cache := readyZoxideCache(nil, SourceMetrics{ZoxideOutcome: "ok"})
	var factoryCalls atomic.Int32
	builder := &Builder{}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		if factoryCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		return cache, nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := builder.LoadInitialZoxide(context.Background())
		firstDone <- err
	}()
	<-started

	cause := errors.New("cancelled behind fresh permit")
	waiterContext, cancelWaiter := context.WithCancelCause(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := builder.LoadInitialZoxide(waiterContext)
		waiterDone <- err
	}()
	cancelWaiter(cause)
	if err := <-waiterDone; !errors.Is(err, cause) {
		t.Fatalf("waiter error=%v, want %v", err, cause)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls while permit held=%d, want 1", got)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if _, err := builder.LoadInitialZoxide(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory calls after permit release=%d, want 2", got)
	}
}

func TestBuildReturnsFreshCacheErrorAndCancelsBlockedLocal(t *testing.T) {
	localStarted := make(chan struct{})
	factoryStarted := make(chan struct{})
	localCancelled := make(chan struct{})
	var localCancelOnce sync.Once
	want := errors.New("fresh cache creation failed")
	builder := &Builder{enumerate: func(ctx context.Context, _ protocol.Picker, _ pathutil.Location, _ LocalOptions) ([]Record, error) {
		close(localStarted)
		<-ctx.Done()
		localCancelOnce.Do(func() { close(localCancelled) })
		return nil, ctx.Err()
	}}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		<-localStarted
		close(factoryStarted)
		return nil, want
	})
	done := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
		done <- struct {
			result BuildResult
			err    error
		}{result, err}
	}()
	<-factoryStarted

	var completed struct {
		result BuildResult
		err    error
	}
	select {
	case completed = <-done:
		if !errors.Is(completed.err, want) || len(completed.result.Records) != 0 {
			t.Fatalf("result=%+v err=%v, want fresh cache error", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Build remained blocked after fresh cache creation failed")
	}
	select {
	case <-localCancelled:
	case <-time.After(time.Second):
		t.Fatalf("Build returned without cancelling and joining local enumeration: err=%v", completed.err)
	}
}

func TestBuildInitialCompatibilityMergesLocalBeforeCachedZoxide(t *testing.T) {
	cache := readyZoxideCache([]Record{
		newRecord(protocol.KindZoxide, "same-zoxide", []byte("/same")),
		newRecord(protocol.KindZoxide, "zoxide", []byte("/zoxide")),
	}, SourceMetrics{ZoxideOutcome: "ok", ZoxideAttempts: 1})
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
		return []Record{
			newRecord(protocol.KindLocal, "local", []byte("/local")),
			newRecord(protocol.KindLocal, "same-local", []byte("/same")),
		}, nil
	}}

	got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		newRecord(protocol.KindLocal, "local", []byte("/local")).FullKey(),
		newRecord(protocol.KindLocal, "same-local", []byte("/same")).FullKey(),
		newRecord(protocol.KindZoxide, "zoxide", []byte("/zoxide")).FullKey(),
	}
	if !reflect.DeepEqual(fullKeys(got.Records), want) || got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "ok" {
		t.Fatalf("result=%+v, want keys=%q", got, want)
	}
}

func TestBuilderIgnoresGenerationSemantically(t *testing.T) {
	cache, _ := newObservedCache(t, "printf '/z/one\\n'\n", zoxideFixtureTimeout, nil)
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

func fullKeys(records []Record) []string {
	result := make([]string, len(records))
	for index := range records {
		result[index] = records[index].FullKey()
	}
	return result
}

func TestCachedPolicyNoninitialBuildIsLocalOnlyWithoutReadyCache(t *testing.T) {
	cache, _ := newObservedCache(t, "printf '/z/one\\n'\n", zoxideFixtureTimeout, nil)
	builder := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: testLocal}
	got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
	if err != nil || !reflect.DeepEqual(paths(got.Records), []string{"/local", "/z/same"}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "not-run" || got.Metrics.ZoxideAttempts != 0 ||
		got.Metrics.ZoxideStarts != 0 || got.Metrics.ZoxideExits != 0 || got.Metrics.ZoxideProcesses != 0 ||
		got.Metrics.ZoxideLive != 0 || got.Metrics.ZoxideMaxLive != 0 {
		t.Fatalf("noninitial metrics=%+v discarded=%v", got.Metrics, got.ZoxideDiscarded)
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
	stale, _ := newObservedCache(t, "printf '/stale\\n'\n", zoxideFixtureTimeout, nil)
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

func TestFreshPolicyNoninitialBuildSkipsFactoryPermitAndProcess(t *testing.T) {
	var factoryCalls atomic.Int32
	builder := &Builder{enumerate: testLocal}
	builder.ConfigureFresh(func() (*ZoxideCache, error) {
		factoryCalls.Add(1)
		t.Error("fresh factory called for noninitial build")
		return nil, errors.New("fresh factory called for noninitial build")
	})
	<-builder.freshPermit
	type buildResponse struct {
		result BuildResult
		err    error
	}
	done := make(chan buildResponse, 1)
	go func() {
		result, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, false))
		done <- buildResponse{result: result, err: err}
	}()
	var response buildResponse
	select {
	case response = <-done:
	case <-time.After(100 * time.Millisecond):
		builder.freshPermit <- struct{}{}
		<-done
		t.Fatal("noninitial build blocked on fresh permit")
	}
	if response.err != nil || !reflect.DeepEqual(paths(response.result.Records), []string{"/local", "/z/same"}) {
		t.Fatalf("got=%+v err=%v", response.result, response.err)
	}
	got := response.result
	if got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "not-run" || got.Metrics.ZoxideAttempts != 0 ||
		got.Metrics.ZoxideStarts != 0 || got.Metrics.ZoxideExits != 0 || got.Metrics.ZoxideProcesses != 0 ||
		got.Metrics.ZoxideLive != 0 || got.Metrics.ZoxideMaxLive != 0 {
		t.Fatalf("noninitial metrics=%+v discarded=%v", got.Metrics, got.ZoxideDiscarded)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory calls=%d", factoryCalls.Load())
	}
}
