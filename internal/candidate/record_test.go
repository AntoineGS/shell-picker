package candidate

import (
	"bytes"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestCompactHomeDisplaysChangesOnlyZoxidePresentation(t *testing.T) {
	home := []byte(t.TempDir())
	path := []byte(filepath.Join(string(home), "projects", "app"))
	wantDisplay := "~/projects/app"
	if runtime.GOOS == "windows" {
		wantDisplay = `~\projects\app`
	}
	zoxide := newRecord(protocol.KindZoxide, protocol.EscapeDisplay(path), path)
	local := newRecord(protocol.KindLocal, ".", home)
	records := []Record{zoxide, local}

	CompactHomeDisplays(records, home)

	if records[0].Display != wantDisplay || records[0].Kind != zoxide.Kind ||
		records[0].Target.Kind != zoxide.Target.Kind || !bytes.Equal(records[0].Path, zoxide.Path) ||
		!bytes.Equal(records[0].Target.Path, zoxide.Target.Path) || records[0].Payload != zoxide.Payload {
		t.Fatalf("zoxide record changed beyond display: got=%+v want=%+v", records[0], zoxide)
	}
	if !reflect.DeepEqual(records[1], local) {
		t.Fatalf("local record changed: got=%+v want=%+v", records[1], local)
	}
}
