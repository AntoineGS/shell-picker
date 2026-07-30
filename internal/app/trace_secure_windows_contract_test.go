//go:build linux

package app

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsTraceSinkSourceContract(t *testing.T) {
	raw, err := os.ReadFile("trace_secure_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{"OPEN_EXISTING", "FILE_FLAG_OPEN_REPARSE_POINT", "GetFileType", "FILE_TYPE_DISK",
		"FILE_ATTRIBUTE_REPARSE_POINT", "WRITE_DAC", "SetSecurityInfo", "PROTECTED_DACL_SECURITY_INFORMATION"} {
		if !strings.Contains(source, required) {
			t.Errorf("Windows trace sink source lacks %s", required)
		}
	}
	if strings.Contains(source, "os.OpenFile") || strings.Contains(source, "O_TRUNC") || strings.Contains(source, "O_CREATE") {
		t.Error("Windows trace sink uses pathname-level create/truncate APIs")
	}
}
