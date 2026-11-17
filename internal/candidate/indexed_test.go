package candidate

import (
	"context"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestBuildIndexedKeepsDirectoriesOnlyForCDAndPreservesPathBytes(t *testing.T) {
	database := &fsearchdb.Database{Entries: []fsearchdb.Entry{
		{Path: []byte("/work/dir"), IsDir: true},
		{Path: []byte("/work/one\tname"), IsDir: false},
		{Path: []byte{'/', 'w', 'o', 'r', 'k', '/', 0xff}, IsDir: false},
	}}
	records, err := BuildIndexed(context.Background(), BuildRequest{Picker: protocol.PickerCD}, database)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != protocol.KindDirectory ||
		string(records[0].Path) != "/work/dir" {
		t.Fatalf("cd records = %+v", records)
	}

	records, err = BuildIndexed(context.Background(), BuildRequest{Picker: protocol.PickerSearch}, database)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("search record count = %d, want 3", len(records))
	}
	found := false
	escapedTab := false
	for _, record := range records {
		if string(record.Path) == string(database.Entries[2].Path) && record.Kind == protocol.KindFile {
			found = true
		}
		if string(record.Path) == string(database.Entries[1].Path) {
			escapedTab = record.Display == "/work/one\\tname"
		}
	}
	if !found {
		t.Fatalf("arbitrary path bytes were lost: %+v", records)
	}
	if !escapedTab {
		t.Fatalf("tab in full path was not escaped: %+v", records)
	}
	records, err = BuildIndexed(context.Background(), BuildRequest{Picker: protocol.PickerSearch, FilesOnly: true}, database)
	if err != nil || len(records) != 2 {
		t.Fatalf("files-only records=%+v err=%v", records, err)
	}
	for _, record := range records {
		if record.Kind != protocol.KindFile {
			t.Fatalf("folder admitted to files-only search: %+v", record)
		}
	}
}
