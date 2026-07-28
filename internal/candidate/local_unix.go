//go:build !windows

package candidate

import (
	"context"
	"errors"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func rootRecords(picker protocol.Picker, location pathutil.Location) []Record {
	return ordinaryRootRecords(picker, location)
}

func enumerateDrives(context.Context) ([]Record, error) {
	return nil, errors.New("drive enumeration is unavailable on this platform")
}
