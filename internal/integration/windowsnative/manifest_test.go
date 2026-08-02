package windowsnative

import (
	"regexp"
	"strings"
	"testing"
)

func TestManifestHasUniquePackagesAndAnchoredPatterns(t *testing.T) {
	seen := make(map[string]struct{})
	for _, pkg := range Packages {
		if _, exists := seen[pkg.Path]; exists {
			t.Errorf("duplicate package %s", pkg.Path)
		}
		seen[pkg.Path] = struct{}{}
		if !strings.HasPrefix(pkg.Pattern, "^(") || !strings.HasSuffix(pkg.Pattern, ")$") {
			t.Errorf("package %s pattern is not exactly anchored: %q", pkg.Path, pkg.Pattern)
		}
		if _, err := regexp.Compile(pkg.Pattern); err != nil {
			t.Errorf("package %s pattern: %v", pkg.Path, err)
		}
	}
}

func TestManifestRejectsUnixAuthorities(t *testing.T) {
	for _, rejected := range []string{"ForegroundTreeOwnsTTY", "Kqueue", "SymlinkInBaseAncestry", "ParityPreviewResourceProcess"} {
		for _, pkg := range Packages {
			if strings.Contains(pkg.Pattern, rejected) {
				t.Errorf("Windows manifest contains Unix authority %s", rejected)
			}
		}
	}
}

func TestManifestValidates(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsInvalidManifests(t *testing.T) {
	original := Packages
	t.Cleanup(func() { Packages = original })

	tests := []struct {
		name     string
		packages []Package
	}{
		{name: "empty manifest"},
		{
			name:     "empty package path",
			packages: []Package{{Path: "", Pattern: `^(TestValid)$`}},
		},
		{
			name: "duplicate package path",
			packages: []Package{
				{Path: "./internal/session", Pattern: `^(TestFirst)$`},
				{Path: "./internal/session", Pattern: `^(TestSecond)$`},
			},
		},
		{
			name:     "unanchored pattern",
			packages: []Package{{Path: "./internal/session", Pattern: `TestUnanchored`}},
		},
		{
			name:     "invalid pattern",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestInvalid$`}},
		},
		{
			name:     "Unix authority",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestKqueue)$`}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Packages = tt.packages
			if err := Validate(); err == nil {
				t.Fatal("Validate returned nil for invalid manifest")
			}
		})
	}
}
