package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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

type parityGoldenMetadata struct {
	IntentionalFixes         []string `json:"intentional_fixes"`
	IntentionalSubstitutions []string `json:"intentional_substitutions"`
}

type parityRunner func(*testing.T, parityRow)

var parityRunners = map[string]parityRunner{}

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

func TestEveryParityRowHasExecutableRunner(t *testing.T) {
	rows := loadParityMatrix(t)
	seen := make(map[string]bool, len(rows))
	executed := make(map[string]bool, len(rows))
	var executedMu sync.Mutex
	t.Cleanup(func() {
		executedMu.Lock()
		defer executedMu.Unlock()
		if len(executed) != 371 {
			t.Fatalf("executed=%d, want 371", len(executed))
		}
	})
	for _, row := range rows {
		if seen[row.ID] {
			t.Fatalf("duplicate row ID %s", row.ID)
		}
		seen[row.ID] = true
		runner, ok := parityRunners[row.Runner]
		if !ok {
			t.Fatalf("row %s has unknown runner %q", row.ID, row.Runner)
		}
		t.Run(row.ID, func(t *testing.T) {
			executedMu.Lock()
			executed[row.ID] = true
			executedMu.Unlock()
			runner(t, row)
		})
	}
}

func TestParityManifestCoverage(t *testing.T) {
	const wantManifestSHA256 = "611cfc8ba119df12189d5b61e9c634730c5e1c8952526e9525ecff03a7669d45"
	const wantAssertionsSHA256 = "a00a40371c39b099d4ed1bd3ed705d56baa5d126e8d5a453242d4978a0a89e64"
	rawManifest, err := os.ReadFile("testdata/parity/source-assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	gotManifestSHA256 := fmt.Sprintf("%x", sha256.Sum256(rawManifest))
	if gotManifestSHA256 != wantManifestSHA256 {
		t.Fatalf("manifest SHA-256=%s, want %s", gotManifestSHA256, wantManifestSHA256)
	}

	manifest := loadParityManifest(t)
	rows := loadParityMatrix(t)
	rawAssertions, err := json.Marshal(manifest.Assertions)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(rawAssertions)); got != wantAssertionsSHA256 {
		t.Fatalf("assertion rows SHA-256=%s, want %s", got, wantAssertionsSHA256)
	}
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
		".zshrc":                    "3bc868023693945a97b2e23f8f806ae5bdaa228a9898fb86cd8ad075e559ab18",
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

	wantCategoryGoldens := map[string]string{
		"operations.json":    "4018ef031f9f5294f1c93d6a4cfe515327cccce2e2a927b6b51e4b2cd365095f",
		"slash.json":         "a4353f35d905f7d173408edb7ddd769ef71559d5e3252a195fbe8432900c23eb",
		"modal.json":         "e19c754fe9396444ca7f4838827746819ddc9a2b0c49516a454525cd334d2f40",
		"create.json":        "33a2e4d0f67637d2ab7d38b96861794e43ae56d66eaea3bc5d8938d6148df4d2",
		"preview.json":       "fc8586501996c635152c07f3138794ec24398ec0a042bd63883e0ef010894921",
		"zsh-adapter.json":   "a396d1dbfc358ad7defa890dadcc53b17765cdb996dc643c3d2daf5ad2c5c68c",
		"windows-paths.json": "dcc60dfa9f10daae4e15032488df044e8cc1ef96faafff6f520769743added43",
	}
	for name, wantHash := range wantCategoryGoldens {
		raw, err := os.ReadFile(filepath.Join("testdata", "parity", "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != wantHash {
			t.Fatalf("category golden %q SHA-256=%s, want %s", name, got, wantHash)
		}
	}
}

func TestParityGoldenMetadata(t *testing.T) {
	files := []string{"operations.json", "slash.json", "modal.json", "create.json", "preview.json", "zsh-adapter.json", "windows-paths.json"}
	declared := make(map[string]bool)
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join("testdata", "parity", "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		var metadata parityGoldenMetadata
		if err := json.Unmarshal(raw, &metadata); err != nil {
			t.Fatalf("decode %s metadata: %v", name, err)
		}
		if len(metadata.IntentionalFixes)+len(metadata.IntentionalSubstitutions) == 0 {
			t.Fatalf("%s has no intentional parity metadata", name)
		}
		for _, item := range append(metadata.IntentionalFixes, metadata.IntentionalSubstitutions...) {
			declared[item] = true
		}
	}
	for _, required := range []string{
		"deterministic non-locale sort", "no legacy cache basename collisions", "useful missing-tool fallbacks instead of blank output",
		"full-record authorization", "drive and UNC share roots use virtual ..", "transactional state replacement",
		"safe Add traversal/rollback", "strict callback/action grammar", "bounded preview/archive/cache resources",
	} {
		if !declared[required] {
			t.Errorf("missing intentional parity declaration %q", required)
		}
	}
}
