package app

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestFrameCandidateRecordsAllocationCeiling(t *testing.T) {
	records := benchmarkCandidateRecords(128)
	var framed []byte
	allocations := testing.AllocsPerRun(100, func() {
		framed = frameCandidateRecords(records)
	})
	runtime.KeepAlive(framed)
	if allocations > 1 {
		t.Fatalf("frameCandidateRecords() allocations = %.0f; want at most 1", allocations)
	}
}

func BenchmarkFrameCandidateRecords(b *testing.B) {
	for _, count := range []int{256, 10_000} {
		records := benchmarkCandidateRecords(count)
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = frameCandidateRecords(records)
			}
		})
	}
}

func benchmarkCandidateRecords(count int) []candidate.Record {
	records := make([]candidate.Record, count)
	for i := range records {
		path := []byte("/tmp/candidate-" + strconv.Itoa(i))
		records[i] = candidate.Record{Kind: protocol.KindFile, Display: string(path), Payload: protocol.EncodePath(path)}
	}
	return records
}
