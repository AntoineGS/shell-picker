package candidate

import (
	"bytes"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type Record struct {
	Kind    protocol.Kind
	Display string
	Path    []byte
	Payload string
}

func (record Record) Wire() protocol.WireRecord {
	return protocol.WireRecord{
		Kind:    record.Kind,
		Display: record.Display,
		Payload: record.Payload,
	}
}

func (record Record) FullKey() string {
	return string(record.Wire().Bytes())
}

func newRecord(kind protocol.Kind, display string, path []byte) Record {
	clonedPath := bytes.Clone(path)
	return Record{
		Kind:    kind,
		Display: display,
		Path:    clonedPath,
		Payload: protocol.EncodePath(clonedPath),
	}
}
