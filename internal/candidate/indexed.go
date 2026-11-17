package candidate

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
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
	seen := make(map[string]struct{}, len(database.Entries))
	for _, entry := range database.Entries {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(string(entry.Path)) {
			return nil, fmt.Errorf("indexed candidates: non-absolute path %q", entry.Path)
		}
		if picker == protocol.PickerCD && !entry.IsDir {
			continue
		}
		if request.FilesOnly && entry.IsDir {
			continue
		}
		key := filesystemRecordKey(entry.Path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		kind := protocol.KindFile
		if entry.IsDir {
			kind = protocol.KindDirectory
		}
		path := append([]byte(nil), entry.Path...)
		records = append(records, Record{
			Kind:    kind,
			Display: protocol.EscapeDisplay(path),
			Path:    path,
			Payload: protocol.EncodePath(path),
			Target:  pathutil.Filesystem(path),
		})
	}
	return records, nil
}
