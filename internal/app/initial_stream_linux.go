//go:build linux

package app

import (
	"context"
	"fmt"
	"io"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const (
	initialStreamBatchSize  = 1024
	initialStreamBatchBytes = 256 << 10
)

func supportsInitialIndexedStream() bool { return true }

func (source *indexedSource) streamInitial(ctx context.Context, request candidate.BuildRequest, output io.Writer) (*fsearchdb.Database, []candidate.Record, error) {
	if source.loader != nil {
		database, err := source.load(ctx)
		if err != nil {
			return nil, nil, err
		}
		records, err := candidate.BuildIndexed(ctx, request, database)
		if err != nil {
			return nil, nil, err
		}
		if _, exceeded := indexedFrameSizeExceeds(records, source.maxFrameBytes); exceeded {
			return nil, nil, fmt.Errorf("indexed candidate set exceeds the %d-byte reload limit", source.maxFrameBytes)
		}
		if err := writeIndexedBatches(output, records); err != nil {
			return nil, nil, err
		}
		return database, records, nil
	}

	database := &fsearchdb.Database{Entries: make([]fsearchdb.Entry, 0)}
	builder := candidate.NewIndexedBuilder(request, 0)
	var records []candidate.Record
	batchStart := 0
	var frameSize, batchBytes uint64
	flush := func() error {
		if err := writeIndexedBatches(output, records[batchStart:]); err != nil {
			return err
		}
		batchStart = len(records)
		batchBytes = 0
		return nil
	}
	err := streamUnearth(ctx, source.home, source.environment, func(entry fsearchdb.Entry) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		database.Entries = append(database.Entries, entry)
		record, ok, err := builder.Add(entry)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		wire := record.Wire()
		size := uint64(len(wire.Kind) + len(wire.Display) + len(wire.Payload) + 3)
		frameSize += size
		batchBytes += size
		if frameSize > source.maxFrameBytes {
			return fmt.Errorf("indexed candidate set exceeds the %d-byte reload limit", source.maxFrameBytes)
		}
		records = append(records, record)
		if len(records)-batchStart >= initialStreamBatchSize || batchBytes >= initialStreamBatchBytes || len(records) == 1 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if err := flush(); err != nil {
		return nil, nil, err
	}
	return database, records, nil
}

func writeIndexedBatches(output io.Writer, records []candidate.Record) error {
	for start := 0; start < len(records); start += initialStreamBatchSize {
		end := min(start+initialStreamBatchSize, len(records))
		framed := protocol.FrameRecordValues(records[start:end])
		n, err := output.Write(framed)
		if err != nil {
			return err
		}
		if n != len(framed) {
			return io.ErrShortWrite
		}
	}
	return nil
}
