package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type IndexLoader func(context.Context, string) (*fsearchdb.Database, error)

// indexedSource owns one decoded saved database for the lifetime of a picker
// session. Failed and canceled attempts are intentionally not cached.
type indexedSource struct {
	home   string
	lookup func(string) string
	loader IndexLoader

	mu            sync.Mutex
	database      *fsearchdb.Database
	loading       chan struct{}
	maxFrameBytes uint64
}

func newIndexedSource(home string, environment []string, loader IndexLoader) *indexedSource {
	values := make(map[string]string)
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	return &indexedSource{
		home: home,
		lookup: func(key string) string {
			if value, ok := values[key]; ok {
				return value
			}
			return ""
		},
		loader:        loader,
		maxFrameBytes: uint64(sessionipc.MaxLoadResponseBytes),
	}
}

func (source *indexedSource) load(ctx context.Context) (*fsearchdb.Database, error) {
	for {
		source.mu.Lock()
		if source.database != nil {
			database := source.database
			source.mu.Unlock()
			return database, nil
		}
		if waiting := source.loading; waiting != nil {
			source.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			case <-waiting:
				continue
			}
		}
		waiting := make(chan struct{})
		source.loading = waiting
		source.mu.Unlock()

		path := fsearchdb.DefaultPath(source.home, source.lookup)
		loader := source.loader
		if loader == nil {
			loader = fsearchdb.Load
		}
		database, err := loader(ctx, path)
		if cause := context.Cause(ctx); cause != nil {
			database = nil
			err = cause
		}

		source.mu.Lock()
		if err == nil && database != nil {
			source.database = database
		} else if err == nil {
			err = errors.New("FSearch database loader returned nil")
		}
		source.loading = nil
		close(waiting)
		source.mu.Unlock()
		return database, err
	}
}

func (source *indexedSource) build(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
	database, err := source.load(ctx)
	if err != nil {
		return candidate.BuildResult{}, err
	}
	records, err := candidate.BuildIndexed(ctx, request, database)
	if err != nil {
		return candidate.BuildResult{}, err
	}
	if size, exceeded := indexedFrameSizeExceeds(records, source.maxFrameBytes); exceeded {
		return candidate.BuildResult{}, fmt.Errorf("FSearch candidate set (%d bytes) exceeds the %d-byte reload limit",
			size, source.maxFrameBytes)
	}
	return candidate.BuildResult{Records: records}, nil
}

func indexedFrameSize(records []candidate.Record) uint64 {
	size, _ := indexedFrameSizeExceeds(records, ^uint64(0))
	return size
}

func indexedFrameSizeExceeds(records []candidate.Record, limit uint64) (uint64, bool) {
	var size uint64
	for _, record := range records {
		wire := record.Wire()
		size += uint64(len(wire.Kind) + len(wire.Display) + len(wire.Payload) + 3)
		if size > limit {
			return size, true
		}
	}
	return size, false
}

func (source *indexedSource) header(picker protocol.Picker, filesOnly bool) string {
	source.mu.Lock()
	database := source.database
	source.mu.Unlock()
	label := "FSearch"
	if filesOnly {
		label += " · files only"
	} else if picker == protocol.PickerCD {
		label += " · directories only"
	}
	if database != nil && !database.SavedAt.IsZero() {
		label += " · saved " + database.SavedAt.Format(time.RFC3339)
	}
	return label
}

func sessionGenerator(options PickerOptions, buildLocal func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error),
	indexed *indexedSource, trace *pickerTrace) session.GenerateFunc {
	return func(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		trace.event(integrationpkg.TraceEvent{Name: "generation.start", Generation: request.Generation, Outcome: "ok"})
		var result candidate.BuildResult
		var err error
		if request.Source == protocol.SourceIndexed {
			result, err = indexed.build(ctx, request)
		} else {
			result, err = buildLocal(ctx, request)
			// An unfinished or inaccessible directory is an empty completion
			// view. Keep the typed path so editing it can recover in-place.
			var pathError *os.PathError
			if options.Completion && errors.As(err, &pathError) && context.Cause(ctx) == nil {
				result.Records, err = nil, nil
			}
		}
		if err == nil {
			candidate.CompactHomeDisplays(result.Records, options.Home)
		} else {
			trace.event(integrationpkg.TraceEvent{Name: "generation.discard", Generation: request.Generation,
				Outcome: traceDiscardOutcome(err)})
		}
		return result, err
	}
}
