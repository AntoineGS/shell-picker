package candidate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type BuildRequest struct {
	Generation  uint64
	Picker      protocol.Picker
	Location    pathutil.Location
	StatWorkers int
	Initial     bool
}

type BuildResult struct {
	Records         []Record
	ZoxideDiscarded bool
	Metrics         SourceMetrics
}

// InitialZoxideResult contains the terminal result of an initial zoxide load.
type InitialZoxideResult struct {
	Records   []Record
	Discarded bool
	Metrics   SourceMetrics
}

type Builder struct {
	Cache    *ZoxideCache
	Policy   ZoxidePolicy
	NewCache func() (*ZoxideCache, error)

	enumerate   func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error)
	freshPermit chan struct{}
}

// ConfigureCached installs session-owned cached-policy state before the first Build.
func (builder *Builder) ConfigureCached(cache *ZoxideCache) {
	builder.Cache = cache
	builder.Policy = ZoxideCached
	builder.NewCache = nil
	builder.freshPermit = nil
}

// ConfigureFresh installs a factory and one session-owned permit before the first Build.
func (builder *Builder) ConfigureFresh(newCache func() (*ZoxideCache, error)) {
	permit := make(chan struct{}, 1)
	permit <- struct{}{}
	builder.Cache = nil
	builder.Policy = ZoxideFresh
	builder.NewCache = newCache
	builder.freshPermit = permit
}

// BuildLocal enumerates only local candidates for request.
func (builder *Builder) BuildLocal(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if err := builder.validate(); err != nil {
		return BuildResult{}, err
	}
	if err := validateBuildRequest(ctx, request); err != nil {
		return BuildResult{}, err
	}
	enumerate := builder.enumerate
	if enumerate == nil {
		enumerate = EnumerateLocal
	}
	return buildLocalOnly(ctx, request, enumerate)
}

// LoadInitialZoxide performs one policy-selected initial zoxide load.
func (builder *Builder) LoadInitialZoxide(ctx context.Context) (InitialZoxideResult, error) {
	if err := builder.validate(); err != nil {
		return InitialZoxideResult{}, err
	}
	if err := validateContext(ctx); err != nil {
		return InitialZoxideResult{}, err
	}

	cache := builder.Cache
	if builder.Policy == ZoxideFresh {
		if err := builder.acquireFreshPermit(ctx); err != nil {
			return InitialZoxideResult{}, err
		}
		defer builder.releaseFreshPermit()
		var err error
		cache, err = builder.NewCache()
		if err != nil {
			return InitialZoxideResult{}, fmt.Errorf("create fresh zoxide cache: %w", err)
		}
		if cache == nil {
			return InitialZoxideResult{}, errors.New("create fresh zoxide cache: nil cache")
		}
	}

	return loadInitialZoxide(ctx, cache)
}

func (builder *Builder) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if err := builder.validate(); err != nil {
		return BuildResult{}, err
	}
	if err := validateBuildRequest(ctx, request); err != nil {
		return BuildResult{}, err
	}
	if request.Picker == protocol.PickerCP || !request.Initial {
		return builder.BuildLocal(ctx, request)
	}
	return builder.buildInitial(ctx, request)
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("candidate builder: nil context")
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return nil
}

func validateBuildRequest(ctx context.Context, request BuildRequest) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if request.Picker != protocol.PickerCD && request.Picker != protocol.PickerCP {
		return fmt.Errorf("unsupported picker %q", request.Picker)
	}
	return nil
}

func (builder *Builder) acquireFreshPermit(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-builder.freshPermit:
		if cause := context.Cause(ctx); cause != nil {
			builder.releaseFreshPermit()
			return cause
		}
		return nil
	}
}

func (builder *Builder) releaseFreshPermit() {
	builder.freshPermit <- struct{}{}
}

func (builder *Builder) buildInitial(ctx context.Context, request BuildRequest) (BuildResult, error) {
	return buildWithZoxide(ctx, request, builder)
}

func (builder *Builder) validate() error {
	if builder == nil {
		return errors.New("candidate builder: nil builder")
	}
	switch builder.Policy {
	case ZoxideCached:
		if builder.Cache == nil || builder.NewCache != nil {
			return errors.New("cached zoxide policy requires Cache and forbids NewCache")
		}
	case ZoxideFresh:
		if builder.Cache != nil || builder.NewCache == nil || builder.freshPermit == nil {
			return errors.New("fresh zoxide policy requires explicit configuration")
		}
	default:
		return fmt.Errorf("invalid zoxide policy %d", builder.Policy)
	}
	return nil
}

type localBuildResult struct {
	records  []Record
	duration time.Duration
	err      error
}

func buildLocalOnly(
	ctx context.Context,
	request BuildRequest,
	enumerate func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error),
) (BuildResult, error) {
	started := time.Now()
	records, err := enumerate(ctx, request.Picker, request.Location, LocalOptions{StatWorkers: request.StatWorkers})
	metrics := SourceMetrics{LocalDuration: time.Since(started), ZoxideOutcome: "not-run"}
	if cause := context.Cause(ctx); cause != nil {
		return BuildResult{}, cause
	}
	return BuildResult{Records: records, Metrics: metrics}, err
}

func buildWithZoxide(
	ctx context.Context,
	request BuildRequest,
	builder *Builder,
) (BuildResult, error) {
	buildCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	localDone := make(chan localBuildResult, 1)
	zoxideDone := make(chan initialZoxideBuildResult, 1)
	go func() {
		result, err := builder.BuildLocal(buildCtx, request)
		localDone <- localBuildResult{records: result.Records, duration: result.Metrics.LocalDuration, err: err}
	}()
	go func() {
		result, err := builder.LoadInitialZoxide(buildCtx)
		zoxideDone <- initialZoxideBuildResult{result: result, err: err}
	}()

	var local localBuildResult
	var zoxide initialZoxideBuildResult
	var localDoneCh <-chan localBuildResult = localDone
	var zoxideDoneCh <-chan initialZoxideBuildResult = zoxideDone
	var firstErr error
	for firstErr == nil && (localDoneCh != nil || zoxideDoneCh != nil) {
		select {
		case local = <-localDoneCh:
			localDoneCh = nil
			if local.err != nil {
				firstErr = local.err
			}
		case zoxide = <-zoxideDoneCh:
			zoxideDoneCh = nil
			if zoxide.err != nil {
				firstErr = zoxide.err
			}
		}
	}
	if firstErr != nil {
		cancel(firstErr)
		for localDoneCh != nil || zoxideDoneCh != nil {
			select {
			case local = <-localDoneCh:
				localDoneCh = nil
			case zoxide = <-zoxideDoneCh:
				zoxideDoneCh = nil
			}
		}
		if cause := context.Cause(ctx); cause != nil {
			return BuildResult{}, cause
		}
		return BuildResult{}, firstErr
	}
	if zoxide.result.Metrics.ZoxideOutcome != "timeout" {
		if cause := context.Cause(ctx); cause != nil {
			return BuildResult{}, cause
		}
	}
	metrics := zoxide.result.Metrics
	metrics.LocalDuration = local.duration
	return BuildResult{
		Records:         MergeRecords(local.records, zoxide.result.Records),
		ZoxideDiscarded: zoxide.result.Discarded,
		Metrics:         metrics,
	}, nil
}

type initialZoxideBuildResult struct {
	result InitialZoxideResult
	err    error
}

func loadInitialZoxide(ctx context.Context, cache *ZoxideCache) (InitialZoxideResult, error) {
	loadErr := cache.Load(ctx)
	if loadErr != nil {
		var waiterCancellation *zoxideWaiterCancellationError
		if errors.As(loadErr, &waiterCancellation) {
			return InitialZoxideResult{}, loadErr
		}
	}
	records, metrics, recordsErr := cache.Records()
	result := InitialZoxideResult{
		Records:   records,
		Discarded: metrics.ZoxideOutcome != "ok" && metrics.ZoxideOutcome != "cached",
		Metrics:   metrics,
	}
	if metrics.ZoxideOutcome == "cancelled" {
		if loadErr != nil {
			return result, loadErr
		}
		return result, recordsErr
	}
	if metrics.ZoxideOutcome != "timeout" {
		if cause := context.Cause(ctx); cause != nil {
			return InitialZoxideResult{}, cause
		}
	}
	if recordsErr != nil && metrics.ZoxideOutcome == "" {
		return InitialZoxideResult{}, recordsErr
	}
	return result, nil
}

// MergeRecords returns local-first records with authoritative target deduplication.
func MergeRecords(local, zoxide []Record) []Record {
	merged, _ := mergeRecords(local, zoxide, false)
	return merged
}

// MergeNewRecords returns base-first records and the additions admitted by target identity.
func MergeNewRecords(base, additions []Record) (merged, admitted []Record) {
	return mergeRecords(base, additions, true)
}

func mergeRecords(base, additions []Record, collectAdmitted bool) (merged, admitted []Record) {
	merged = make([]Record, 0, len(base)+len(additions))
	if collectAdmitted {
		admitted = make([]Record, 0, len(additions))
	}
	filesystem := make(map[string]struct{}, len(base)+len(additions))
	virtual := make(map[virtualRecordKey]struct{})
	appendRecord := func(record Record, isAddition bool) {
		if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem {
			key := virtualRecordKey{kind: record.Target.Kind, target: string(record.Target.Path), wire: record.FullKey()}
			if _, exists := virtual[key]; exists {
				return
			}
			virtual[key] = struct{}{}
			merged = append(merged, record)
			if isAddition && collectAdmitted {
				admitted = append(admitted, record)
			}
			return
		}
		key := filesystemRecordKey(record.Target.Path)
		if _, exists := filesystem[key]; exists {
			return
		}
		filesystem[key] = struct{}{}
		merged = append(merged, record)
		if isAddition && collectAdmitted {
			admitted = append(admitted, record)
		}
	}
	for _, record := range base {
		appendRecord(record, false)
	}
	for _, record := range additions {
		appendRecord(record, true)
	}
	return merged, admitted
}
