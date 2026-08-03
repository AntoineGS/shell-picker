package app

import (
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func frameCandidateRecords(records []candidate.Record) []byte {
	wire := make([]protocol.WireRecord, len(records))
	for index, record := range records {
		wire[index] = record.Wire()
	}
	return protocol.FrameRecords(wire)
}
