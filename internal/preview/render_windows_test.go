//go:build windows

package preview

import (
	"bytes"
	"testing"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
)

func TestExternalRendererSpecRequestsWindowsNestedJob(t *testing.T) {
	var stdout, stderr bytes.Buffer
	spec := externalProcessSpec(`C:\tools\bat.exe`, []string{"--", `C:\data\file.txt`}, []string{`PATH=C:\tools`}, &stdout, &stderr)
	if spec.Containment != processpkg.ContainmentInheritTree || spec.Stdout != &stdout || spec.Stderr != &stderr {
		t.Fatalf("spec=%+v", spec)
	}
}
