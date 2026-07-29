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

func (builder *Builder) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if err := builder.validate(); err != nil {
		return BuildResult{}, err
	}
	if ctx == nil {
		return BuildResult{}, errors.New("candidate builder: nil context")
	}
	if cause := context.Cause(ctx); cause != nil {
		return BuildResult{}, cause
	}
	enumerate := builder.enumerate
	if enumerate == nil {
		enumerate = EnumerateLocal
	}
	if request.Picker == protocol.PickerCP {
		started := time.Now()
		records, err := enumerate(ctx, request.Picker, request.Location, LocalOptions{StatWorkers: request.StatWorkers})
		metrics := SourceMetrics{LocalDuration: time.Since(started), ZoxideOutcome: "not-run"}
		if cause := context.Cause(ctx); cause != nil {
			return BuildResult{}, cause
		}
		return BuildResult{Records: records, Metrics: metrics}, err
	}
	if request.Picker != protocol.PickerCD {
		return BuildResult{}, fmt.Errorf("unsupported picker %q", request.Picker)
	}

	if builder.Policy == ZoxideFresh {
		if err := builder.acquireFreshPermit(ctx); err != nil {
			return BuildResult{}, err
		}
		defer builder.releaseFreshPermit()
		cache, err := builder.NewCache()
		if err != nil {
			return BuildResult{}, fmt.Errorf("create fresh zoxide cache: %w", err)
		}
		if cache == nil {
			return BuildResult{}, errors.New("create fresh zoxide cache: nil cache")
		}
		return buildWithZoxide(ctx, request, enumerate, cache, true)
	}
	if request.Initial {
		return buildWithZoxide(ctx, request, enumerate, builder.Cache, true)
	}
	return builder.buildCachedNavigation(ctx, request, enumerate)
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

func (builder *Builder) validate() error {
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

func buildWithZoxide(
	ctx context.Context,
	request BuildRequest,
	enumerate func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error),
	cache *ZoxideCache,
	reportAttempt bool,
) (BuildResult, error) {
	buildCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	localDone := make(chan localBuildResult, 1)
	zoxideDone := make(chan error, 1)
	go func() {
		started := time.Now()
		records, err := enumerate(buildCtx, request.Picker, request.Location, LocalOptions{StatWorkers: request.StatWorkers})
		localDone <- localBuildResult{records: records, duration: time.Since(started), err: err}
	}()
	go func() { zoxideDone <- cache.Load(buildCtx) }()

	local := <-localDone
	if local.err != nil {
		cancel(local.err)
		<-zoxideDone
		if cause := context.Cause(ctx); cause != nil {
			return BuildResult{}, cause
		}
		return BuildResult{}, local.err
	}
	zoxideErr := <-zoxideDone
	records, metrics, recordsErr := cache.Records()
	metrics.LocalDuration = local.duration
	if !reportAttempt {
		metrics.ZoxideAttempts = 0
		metrics.ZoxideStarts = 0
		metrics.ZoxideMaxLive = 0
	}
	if metrics.ZoxideOutcome == "cancelled" {
		if zoxideErr != nil {
			return BuildResult{}, zoxideErr
		}
		return BuildResult{}, recordsErr
	}
	if metrics.ZoxideOutcome != "timeout" {
		if cause := context.Cause(ctx); cause != nil {
			return BuildResult{}, cause
		}
	}
	return BuildResult{
		Records:         mergeRecords(local.records, records),
		ZoxideDiscarded: metrics.ZoxideOutcome != "ok" && metrics.ZoxideOutcome != "cached",
		Metrics:         metrics,
	}, nil
}

func (builder *Builder) buildCachedNavigation(
	ctx context.Context,
	request BuildRequest,
	enumerate func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error),
) (BuildResult, error) {
	started := time.Now()
	local, err := enumerate(ctx, request.Picker, request.Location, LocalOptions{StatWorkers: request.StatWorkers})
	localDuration := time.Since(started)
	if err != nil {
		return BuildResult{}, err
	}
	if cause := context.Cause(ctx); cause != nil {
		return BuildResult{}, cause
	}
	zoxide, metrics, err := builder.Cache.Records()
	if metrics.ZoxideOutcome == "cancelled" {
		return BuildResult{}, err
	}
	if errors.Is(err, errZoxideNotReady) {
		return BuildResult{}, err
	}
	metrics.LocalDuration = localDuration
	metrics.ZoxideOutcome = "cached"
	metrics.ZoxideDuration = 0
	metrics.ZoxideAttempts = 0
	metrics.ZoxideStarts = 0
	metrics.ZoxideMaxLive = 0
	return BuildResult{Records: mergeRecords(local, zoxide), Metrics: metrics}, nil
}

func mergeRecords(local, zoxide []Record) []Record {
	merged := make([]Record, 0, len(local)+len(zoxide))
	filesystem := make(map[string]struct{}, len(local)+len(zoxide))
	virtual := make(map[virtualRecordKey]struct{})
	appendRecord := func(record Record) {
		if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem {
			key := virtualRecordKey{kind: record.Target.Kind, target: string(record.Target.Path), wire: record.FullKey()}
			if _, exists := virtual[key]; exists {
				return
			}
			virtual[key] = struct{}{}
			merged = append(merged, record)
			return
		}
		key := string(record.Target.Path)
		if _, exists := filesystem[key]; exists {
			return
		}
		filesystem[key] = struct{}{}
		merged = append(merged, record)
	}
	for _, record := range local {
		appendRecord(record)
	}
	for _, record := range zoxide {
		appendRecord(record)
	}
	return merged
}

type virtualRecordKey struct {
	kind   pathutil.Kind
	target string
	wire   string
}
