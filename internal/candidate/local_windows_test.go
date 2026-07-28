//go:build windows

package candidate

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestEnumerateDrivesOrderKindAndIdentity(t *testing.T) {
	original := listLocalDrives
	listLocalDrives = func() ([]pathutil.Location, error) {
		return []pathutil.Location{
			pathutil.Filesystem([]byte(`Z:\`)),
			pathutil.Filesystem([]byte(`A:\`)),
			pathutil.Filesystem([]byte(`D:\`)),
		}, nil
	}
	defer func() { listLocalDrives = original }()

	records, err := EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Drives(), LocalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`A:\`, `D:\`, `Z:\`}
	got := make([]string, len(records))
	for index, record := range records {
		got[index] = record.Display
		if record.Kind != protocol.KindDrive {
			t.Errorf("kind for %q = %q; want %q", record.Display, record.Kind, protocol.KindDrive)
		}
		if record.FullKey() != string(record.Wire().Bytes()) {
			t.Errorf("FullKey for %q is not its exact wire record", record.Display)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drive displays = %q; want %q", got, want)
	}
}

func TestEnumerateDrivesErrorPublishesNothing(t *testing.T) {
	wantErr := errors.New("drive discovery failed")
	original := listLocalDrives
	listLocalDrives = func() ([]pathutil.Location, error) { return nil, wantErr }
	defer func() { listLocalDrives = original }()

	records, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Drives(), LocalOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v; want wrapped %v", err, wantErr)
	}
	if records != nil {
		t.Fatalf("records = %+v; want no partial publication", records)
	}
}
