package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

type parityRow struct {
	ID           string `json:"id"`
	Suite        string `json:"suite"`
	SourceLine   int    `json:"source_line"`
	Runner       string `json:"runner"`
	Case         string `json:"case"`
	Check        string `json:"check"`
	ExpectedText string `json:"expected_text"`
	Golden       string `json:"golden,omitempty"`
}

type parityManifest struct {
	Sources    map[string]string `json:"sources"`
	Assertions []parityRow       `json:"assertions"`
}

func loadParityManifest(t *testing.T) parityManifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/parity/source-assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest parityManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func loadParityMatrix(t *testing.T) []parityRow {
	t.Helper()
	return loadParityManifest(t).Assertions
}

func TestParityManifestCoverage(t *testing.T) {
	manifest := loadParityManifest(t)
	rows := loadParityMatrix(t)
	want := map[string]int{
		"codec": 50, "batch-encoder": 2, "directory-enumeration": 72, "cd-merged": 8,
		"operations": 34, "slash": 28, "modal": 20, "create": 51, "preview": 3,
		"zshrc-cd": 42, "zshrc-cp": 43, "zshrc-add-mode-query-bindings": 6,
		"zshrc-add-mode-navigation-bindings": 12,
	}
	got := map[string]int{}
	ids := map[string]bool{}
	goldenReferences := map[string]bool{}
	for i, row := range rows {
		got[row.Suite]++
		wantID := fmt.Sprintf("SRC-%03d", i+1)
		if ids[row.ID] || row.ID != wantID || row.SourceLine <= 0 || row.Runner == "" || row.Case == "" || row.Check == "" {
			t.Fatalf("invalid row: %+v", row)
		}
		ids[row.ID] = true
		if row.Golden != "" {
			goldenReferences[row.Golden] = true
		}
	}
	if !reflect.DeepEqual(got, want) || len(rows) != 371 {
		t.Fatalf("counts=%v total=%d", got, len(rows))
	}

	wantSources := map[string]string{
		"fzf-picker-candidates.zsh": "5300b66b7815e8b1c2f75f230033a069a1c305600faea164c9214cd52e07cb97",
		"fzf-preview.sh":            "232eb46eef32bff642985e42edbf0cd3a49098e7485eb6f5b0db0bdf48024159",
		"fzf-batch-encode.pl":       "055d9a74cce513bbf02475fae97154b159de98d51cc565cf4859ced0226878fd",
		"fzf-picker.test.zsh":       "f920b8f6194c76d5f8a1737c6e4860ab04f641da291dc3e984cbb63443552776",
		".zshrc":                    "92cc80ec53564642fe8e2f51375ed3108cc8bc0d6f1c52f01e572ee8f716dc94",
	}
	if !reflect.DeepEqual(manifest.Sources, wantSources) {
		t.Fatalf("source hashes=%v", manifest.Sources)
	}

	wantGoldens := map[string]string{
		"golden/batch-encoder.bin": "2841d0c496fa22dc93cbe238fff01386a76819dc956cc0788314c2480e9f57de",
		"golden/cd-order.bin":      "a883ded7822fd71c2a270199f942a0b23ba814bbeefccd9512c3f84896d08ff6",
		"golden/codec-records.bin": "741357fcb0267d9f04d7ea901fc3fb65e3f774b66eb861b07fe82662caf8f5f7",
		"golden/cp-order.bin":      "38bcbbe8066cc5a7c02a79eadf29c19941df601ba224df3bb92f874a6ef4cbee",
	}
	for name, wantHash := range wantGoldens {
		raw, err := os.ReadFile("testdata/parity/" + name)
		if err != nil {
			t.Fatal(err)
		}
		gotHash := fmt.Sprintf("%x", sha256.Sum256(raw))
		if gotHash != wantHash || !goldenReferences[name] {
			t.Fatalf("golden %q hash=%s referenced=%v", name, gotHash, goldenReferences[name])
		}
		delete(goldenReferences, name)
	}
	if len(goldenReferences) != 0 {
		t.Fatalf("unrecognized golden references: %v", goldenReferences)
	}
}
