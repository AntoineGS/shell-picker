package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveLimitsEntriesBytesAndDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bomb.zip")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for index := 0; index < 101; index++ {
		entry, err := zw.Create(fmt.Sprintf("entry-%03d-%s", index, strings.Repeat("x", 32)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(strings.Repeat("z", 48<<10)))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	options := testOptions(&output)
	started := time.Now()
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(output.Bytes(), []byte("\n")); lines > DefaultLimits.MaxArchiveEntries+2 {
		t.Fatalf("listed %d lines", lines)
	}
	if output.Len() > int(DefaultLimits.MaxOutputBytes) || time.Since(started) > DefaultLimits.Deadline {
		t.Fatalf("bytes=%d duration=%s", output.Len(), time.Since(started))
	}

	output.Reset()
	options.Limits.Deadline = time.Nanosecond
	if err := Render(context.Background(), resolved(path), options); err != context.DeadlineExceeded {
		t.Fatalf("deadline err=%v", err)
	}
}
