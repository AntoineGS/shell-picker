//go:build linux

package app

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsTraceSinkSourceContract(t *testing.T) {
	var source strings.Builder
	for _, path := range []string{"trace_secure_windows.go", "trace_secure_windows_lock.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source.Write(raw)
	}
	contents := source.String()
	for _, required := range []string{"OPEN_EXISTING", "FILE_FLAG_OPEN_REPARSE_POINT", "GetFileType", "FILE_TYPE_DISK",
		"FILE_ATTRIBUTE_REPARSE_POINT", "WRITE_DAC", "SetSecurityInfo", "PROTECTED_DACL_SECURITY_INFORMATION",
		"CreateMutex(nil, true", "WaitForSingleObject", "WAIT_ABANDONED", "ReleaseMutex", "LockOSThread", "WriteRecord", "sha256"} {
		if !strings.Contains(contents, required) {
			t.Errorf("Windows trace sink source lacks %s", required)
		}
	}
	if strings.Contains(contents, "os.OpenFile") || strings.Contains(contents, "O_TRUNC") || strings.Contains(contents, "O_CREATE") {
		t.Error("Windows trace sink uses pathname-level create/truncate APIs")
	}
}
