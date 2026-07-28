//go:build windows

package candidate

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestWindowsDriveAndUNCRootsUseCanonicalVirtualDrivesParent(t *testing.T) {
	roots := []pathutil.Location{
		pathutil.Filesystem([]byte(`C:\`)),
		pathutil.Filesystem([]byte(`\\server\share\`)),
	}
	for _, root := range roots {
		for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
			records := rootRecords(picker, root)
			assertWindowsDisplays(t, records, []string{".", ".."})
			for index, record := range records {
				parsed, decoded := parseWindowsRecord(t, record)
				if index == 1 {
					if parsed.Kind != protocol.KindVirtual || parsed.Display != ".." ||
						!bytes.Equal(decoded, []byte("drives")) || record.Target.Kind != pathutil.KindDrives ||
						len(record.Path) != 0 || record.Payload != "ZHJpdmVz" {
						t.Fatalf("virtual picker=%s root=%q record=%+v decoded=%q", picker, root.Path, record, decoded)
					}
				} else if record.Target.Kind != pathutil.KindFilesystem || !bytes.Equal(decoded, record.Path) ||
					!bytes.Equal(record.Target.Path, record.Path) {
					t.Fatalf("ordinary picker=%s root=%q record=%+v decoded=%q", picker, root.Path, record, decoded)
				}
			}
		}
	}
}

func TestWindowsNonRootParentRemainsFilesystemRecord(t *testing.T) {
	location := pathutil.Filesystem([]byte(`C:\users\alice`))
	records := rootRecords(protocol.PickerCP, location)
	assertWindowsDisplays(t, records, []string{".", ".."})
	for _, record := range records {
		parsed, decoded := parseWindowsRecord(t, record)
		if parsed.Kind != protocol.KindDirectory || record.Target.Kind != pathutil.KindFilesystem ||
			!bytes.Equal(decoded, record.Path) || !bytes.Equal(record.Target.Path, record.Path) {
			t.Fatalf("ordinary record=%+v decoded=%q", record, decoded)
		}
	}
	if !bytes.Equal(records[1].Path, []byte(`C:\users`)) {
		t.Fatalf("parent path = %q; want C:\\users", records[1].Path)
	}
}

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
		_, decoded := parseWindowsRecord(t, record)
		if record.Target.Kind != pathutil.KindFilesystem || !bytes.Equal(record.Target.Path, record.Path) ||
			!bytes.Equal(decoded, record.Path) {
			t.Errorf("drive record has non-filesystem target: %+v decoded=%q", record, decoded)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drive displays = %q; want %q", got, want)
	}
}

func parseWindowsRecord(t *testing.T, record Record) (protocol.WireRecord, []byte) {
	t.Helper()
	parsed, err := protocol.ParseRecord(record.Wire().Bytes())
	if err != nil {
		t.Fatalf("ParseRecord(%q): %v", record.Wire().Bytes(), err)
	}
	decoded, err := protocol.DecodePath(parsed.Payload)
	if err != nil || len(decoded) == 0 {
		t.Fatalf("DecodePath(%q) = %q, %v; want nonempty", parsed.Payload, decoded, err)
	}
	return parsed, decoded
}

func assertWindowsDisplays(t *testing.T, records []Record, want []string) {
	t.Helper()
	got := make([]string, len(records))
	for index := range records {
		got[index] = records[index].Display
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("displays = %q; want %q", got, want)
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
