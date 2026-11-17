package integration

import (
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestSecurityGateManifestSelectsEveryRequiredTest(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(task20FocusedPattern)
	for _, pkg := range task20GateManifest {
		required := append([]string(nil), pkg.tests...)
		if runtime.GOOS == "windows" {
			required = append(required, pkg.windows...)
		} else {
			required = append(required, pkg.unix...)
		}
		listed := listPackageTests(t, root, pkg.path)
		selected := 0
		for name := range listed {
			if pattern.MatchString(name) {
				selected++
			}
		}
		t.Logf("%s selected=%d", pkg.path, selected)
		expectedSelected := pkg.selectedUnix
		if runtime.GOOS == "windows" {
			expectedSelected = pkg.selectedWindows
		}
		if selected != expectedSelected {
			t.Errorf("%s: documented focused regex selected=%d want=%d", pkg.path, selected, expectedSelected)
		}
		for _, name := range required {
			if _, ok := listed[name]; !ok {
				t.Errorf("%s: required test %s is absent", pkg.path, name)
			}
			if !pattern.MatchString(name) {
				t.Errorf("%s: documented focused regex omits %s", pkg.path, name)
			}
		}
	}
}
