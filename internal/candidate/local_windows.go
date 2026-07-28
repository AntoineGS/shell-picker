//go:build windows

package candidate

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var listLocalDrives = pathutil.ListDrives

func enumerateDrives(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	drives, err := listLocalDrives()
	if err != nil {
		return nil, fmt.Errorf("list local drives: %w", err)
	}
	sort.Slice(drives, func(left, right int) bool {
		return bytes.Compare(drives[left].Path, drives[right].Path) < 0
	})
	records := make([]Record, 0, len(drives))
	for _, drive := range drives {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		records = append(records, newRecord(protocol.KindDrive, string(drive.Path), drive.Path))
	}
	return records, nil
}
