package candidate

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// BuildIndexed converts a saved FSearch snapshot to authoritative picker
// records without statting indexed paths.
func BuildIndexed(ctx context.Context, request BuildRequest, database *fsearchdb.Database) ([]Record, error) {
	picker := request.Picker
	if ctx == nil {
		return nil, fmt.Errorf("indexed candidates: nil context")
	}
	if database == nil {
		return nil, fmt.Errorf("indexed candidates: nil database")
	}
	if picker != protocol.PickerCD && picker != protocol.PickerCP && picker != protocol.PickerSearch {
		return nil, fmt.Errorf("indexed candidates: unsupported picker %q", picker)
	}
	records := make([]Record, 0, len(database.Entries))
	builder := NewIndexedBuilder(request, len(database.Entries))
	for _, entry := range database.Entries {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		record, ok, err := builder.Add(entry)
		if err != nil {
			return nil, err
		}
		if ok {
			records = append(records, record)
		}
	}
	return records, nil
}

// IndexedBuilder formats and deduplicates indexed paths in input order.
type IndexedBuilder struct {
	request BuildRequest
	seen    map[string]struct{}
}

func NewIndexedBuilder(request BuildRequest, hint int) *IndexedBuilder {
	return &IndexedBuilder{request: request, seen: make(map[string]struct{}, hint)}
}

func (builder *IndexedBuilder) Add(entry fsearchdb.Entry) (Record, bool, error) {
	if !filepath.IsAbs(string(entry.Path)) {
		return Record{}, false, fmt.Errorf("indexed candidates: non-absolute path %q", entry.Path)
	}
	if builder.request.Picker == protocol.PickerCD && !entry.IsDir || builder.request.FilesOnly && entry.IsDir {
		return Record{}, false, nil
	}
	key := filesystemRecordKey(entry.Path)
	if _, exists := builder.seen[key]; exists {
		return Record{}, false, nil
	}
	builder.seen[key] = struct{}{}
	kind := protocol.KindFile
	if entry.IsDir {
		kind = protocol.KindDirectory
	}
	return newRecord(kind, protocol.EscapeDisplay(entry.Path), entry.Path), true, nil
}
