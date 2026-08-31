package candidate

import (
	"bytes"
	"runtime"
	"strings"

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

func zoxideDisplay(path []byte) string {
	display := protocol.EscapeDisplay(path)
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(display, `\\`, `\`)
	}
	return display
}

func CompactHomeDisplays(records []Record, home []byte) {
	for index := range records {
		if records[index].Kind == protocol.KindZoxide {
			records[index].Display = zoxideDisplay(pathutil.CompactHome(records[index].Path, home))
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

func newRecordFromString(kind protocol.Kind, display, path string) Record {
	recordPath := []byte(path)
	return Record{
		Kind:    kind,
		Display: display,
		Path:    recordPath,
		Payload: protocol.EncodePath(recordPath),
		Target:  pathutil.Filesystem(recordPath),
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
