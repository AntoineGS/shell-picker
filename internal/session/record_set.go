package session

import (
	"strings"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// recordSet is immutable after publication so snapshots can share it safely.
type recordSet struct {
	values       []candidate.Record
	byFullRecord map[protocol.WireRecord]int
}

func cloneRecordSet(records []candidate.Record) *recordSet {
	return ownRecordSet(cloneRecords(records))
}

func ownRecordSet(records []candidate.Record) *recordSet {
	return &recordSet{values: records, byFullRecord: buildIndex(records)}
}

func buildIndex(records []candidate.Record) map[protocol.WireRecord]int {
	index := make(map[protocol.WireRecord]int, len(records))
	for position, record := range records {
		key := record.Wire()
		if _, exists := index[key]; !exists {
			index[key] = position
		}
	}
	return index
}

func (records *recordSet) lookup(raw string) (int, bool) {
	kind, rest, ok := strings.Cut(raw, "\t")
	if !ok {
		return 0, false
	}
	display, payload, ok := strings.Cut(rest, "\t")
	if !ok {
		return 0, false
	}
	index, ok := records.byFullRecord[protocol.WireRecord{Kind: protocol.Kind(kind), Display: display, Payload: payload}]
	return index, ok
}
