package app

import (
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func frameCandidateRecords(records []candidate.Record) []byte {
	return protocol.FrameRecordValues(records)
}
