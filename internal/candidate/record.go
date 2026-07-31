package candidate

import (
	"bytes"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type Record struct {
	Kind    protocol.Kind
	Display string
	Path    []byte
	Payload string
	Target  pathutil.Location
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

func CompactHomeDisplays(records []Record, home []byte) {
	for index := range records {
		if records[index].Kind == protocol.KindZoxide {
			records[index].Display = protocol.EscapeDisplay(pathutil.CompactHome(records[index].Path, home))
		}
	}
}

func newRecord(kind protocol.Kind, display string, path []byte) Record {
	clonedPath := bytes.Clone(path)
	return Record{
		Kind:    kind,
		Display: display,
		Path:    clonedPath,
		Payload: protocol.EncodePath(clonedPath),
		Target:  pathutil.Filesystem(clonedPath),
	}
}

func newVirtualDrivesRecord(display string) Record {
	token := []byte(protocol.VirtualDrivesTarget)
	return Record{
		Kind:    protocol.KindVirtual,
		Display: display,
		Payload: protocol.EncodePath(token),
		Target:  pathutil.Drives(),
	}
}
