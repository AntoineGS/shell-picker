//go:build linux

package app

import (
	"bytes"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type countingIndexWriter struct {
	bytes.Buffer
	writes int
}

func (w *countingIndexWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(p)
}

func TestWriteIndexedBatchesMakesOneWritePerBatch(t *testing.T) {
	records := make([]candidate.Record, initialStreamBatchSize+1)
	for i := range records {
		records[i] = candidate.Record{Kind: protocol.KindFile, Display: "/a", Payload: protocol.EncodePath([]byte("/a"))}
	}
	var writer countingIndexWriter
	if err := writeIndexedBatches(&writer, records); err != nil {
		t.Fatal(err)
	}
	if writer.writes != 2 || !bytes.Equal(writer.Bytes(), protocol.FrameRecordValues(records)) {
		t.Fatalf("writes = %d, bytes = %d; want two writes with exact wire records", writer.writes, writer.Len())
	}
}
