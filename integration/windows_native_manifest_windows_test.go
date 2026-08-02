//go:build windows

package integration

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/integration/windowsnative"
)

func TestWindowsNativeManifestSelectsEveryRequiredTest(t *testing.T) {
	if err := windowsnative.Validate(); err != nil {
		t.Fatalf("Windows native manifest: %v", err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range windowsnative.Packages {
		pkg := pkg
		t.Run(pkg.Path, func(t *testing.T) {
			pattern, err := regexp.Compile(pkg.Pattern)
			if err != nil {
				t.Fatalf("compile %s pattern: %v", pkg.Path, err)
			}
			listed := listPackageTests(t, root, pkg.Path)
			selected := 0
			for name := range listed {
				if pattern.MatchString(name) {
					selected++
				}
			}
			if selected == 0 {
				t.Fatalf("%s selected no tests with %s", pkg.Path, pkg.Pattern)
			}
			for _, name := range windowsNativeManifestAlternatives(pkg.Pattern) {
				if _, ok := listed[name]; !ok {
					t.Errorf("%s manifest test %s is absent", pkg.Path, name)
				}
			}
		})
	}
}

func windowsNativeManifestAlternatives(pattern string) []string {
	const (
		prefix = "^("
		suffix = ")$"
	)
	if !strings.HasPrefix(pattern, prefix) || !strings.HasSuffix(pattern, suffix) {
		return nil
	}
	body := strings.TrimSuffix(strings.TrimPrefix(pattern, prefix), suffix)
	return strings.Split(body, "|")
}
