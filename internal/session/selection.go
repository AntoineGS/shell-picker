package session

import (
	"errors"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	ErrInvalidSelection = errors.New("invalid selection")
	ErrUnknownSelection = errors.New("selection is not in the current snapshot")
	ErrInvalidBase      = errors.New("invalid original base")
)

func ValidateCD(snapshot Snapshot, accepted [][]byte) (protocol.Outcome, error) {
	if len(accepted) != 1 {
		return protocol.Outcome{}, ErrInvalidSelection
	}
	_, err := protocol.ParseRecord(accepted[0])
	if err != nil {
		return protocol.Outcome{}, ErrInvalidSelection
	}
	record, ok := snapshot.lookupRecord(string(accepted[0]))
	if !ok {
		return protocol.Outcome{}, ErrUnknownSelection
	}
	if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem || !cdSelectionKind(record.Kind) {
		return protocol.Outcome{}, ErrInvalidSelection
	}
	return protocol.Outcome{Status: protocol.StatusAccepted, Paths: [][]byte{append([]byte(nil), record.Path...)}}, nil
}

func ValidateCP(snapshot Snapshot, accepted [][]byte, base []byte) (protocol.Outcome, error) {
	if len(base) == 0 {
		return protocol.Outcome{}, ErrInvalidBase
	}
	if len(accepted) == 0 {
		return protocol.Outcome{}, ErrInvalidSelection
	}
	counts := make(map[protocol.WireRecord]int, len(accepted))
	for _, raw := range accepted {
		key, err := protocol.ParseRecord(raw)
		if err != nil {
			return protocol.Outcome{}, ErrInvalidSelection
		}
		counts[key]++
	}
	paths := make([][]byte, 0, len(accepted))
	for _, record := range snapshot.recordValues() {
		key := record.Wire()
		if counts[key] == 0 {
			continue
		}
		if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem {
			return protocol.Outcome{}, ErrInvalidSelection
		}
		paths = append(paths, pathutil.Relative(base, record.Path))
		counts[key]--
	}
	if len(paths) != len(accepted) {
		return protocol.Outcome{}, ErrUnknownSelection
	}
	return protocol.Outcome{Status: protocol.StatusAccepted, Paths: paths}, nil
}

func cdSelectionKind(kind protocol.Kind) bool {
	return kind == protocol.KindLocal || kind == protocol.KindDirectory || kind == protocol.KindZoxide || kind == protocol.KindDrive
}
