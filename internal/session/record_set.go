package session

import (
	"github.com/AntoineGS/shell-picker/internal/candidate"
)

// recordSet is immutable after publication so snapshots can share it safely.
type recordSet struct {
	values       []candidate.Record
	byFullRecord map[string]int
}

func cloneRecordSet(records []candidate.Record) *recordSet {
	return ownRecordSet(cloneRecords(records))
}

func ownRecordSet(records []candidate.Record) *recordSet {
	return &recordSet{values: records, byFullRecord: buildIndex(records)}
}

func buildIndex(records []candidate.Record) map[string]int {
	index := make(map[string]int, len(records))
	for position, record := range records {
		key := fullRecordKey(record)
		if _, exists := index[key]; !exists {
			index[key] = position
		}
	}
	return index
}

func fullRecordKey(record candidate.Record) string {
	wire := record.Wire()
	return string(wire.Kind) + "\t" + wire.Display + "\t" + wire.Payload
}
