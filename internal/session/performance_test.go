package session

import (
	"strconv"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func BenchmarkSnapshotClone(b *testing.B) {
	for _, count := range []int{256, 10_000} {
		records := benchmarkRecords(count)
		snapshot := Snapshot{generation: 1, state: State{
			Location: pathutil.Filesystem([]byte("/tmp/location")),
			Home:     pathutil.Filesystem([]byte("/home/benchmark")),
		}, records: cloneRecordSet(records)}
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cloneSnapshot(snapshot)
			}
		})
	}
}

func BenchmarkBuildIndex(b *testing.B) {
	for _, count := range []int{256, 10_000} {
		records := benchmarkRecords(count)
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = buildIndex(records)
			}
		})
	}
}

func benchmarkRecords(count int) []candidate.Record {
	records := make([]candidate.Record, count)
	for i := range records {
		path := []byte("/tmp/candidate-" + strconv.Itoa(i))
		records[i] = candidate.Record{Kind: protocol.KindFile, Display: string(path), Path: path,
			Payload: protocol.EncodePath(path), Target: pathutil.Filesystem(path)}
	}
	return records
}
