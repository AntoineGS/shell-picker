//go:build windows

package session

import (
	"bytes"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestValidateCPWindowsCrossVolumeUsesAbsolutePath(t *testing.T) {
	record := eventRecord(protocol.KindFile, "target", `D:\data\target.txt`)
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeNormal, pathutil.Filesystem([]byte(`D:\data`)), record)
	outcome, err := ValidateCP(snapshot, [][]byte{[]byte(record.FullKey())}, []byte(`C:\work`))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Paths) != 1 || !bytes.Equal(outcome.Paths[0], []byte(`D:\data\target.txt`)) {
		t.Fatalf("paths=%q", outcome.Paths)
	}
}
