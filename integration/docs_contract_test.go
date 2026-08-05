package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestDocumentedProtocolMatchesConstants(t *testing.T) {
	doc := readDoc(t, "docs/protocol.md")
	for _, kind := range []protocol.Kind{protocol.KindLocal, protocol.KindDirectory, protocol.KindFile, protocol.KindZoxide, protocol.KindDrive, protocol.KindVirtual} {
		requireDocumented(t, doc, string(kind))
	}
	for _, opcode := range []protocol.Opcode{protocol.OpModeInsert, protocol.OpModeAdd, protocol.OpEscape, protocol.OpForward, protocol.OpParent, protocol.OpSlash, protocol.OpHome, protocol.OpEnter, protocol.OpRestoreView} {
		requireDocumented(t, doc, "e:"+string(opcode))
	}
	for _, value := range []string{protocol.VirtualDrivesTarget, protocol.EncodePath([]byte(protocol.VirtualDrivesTarget)), "p", "l:<positive decimal generation>", "l:<positive decimal generation>:<positive decimal eventID>", "l:empty", "p:invalid", "/v1/event/finalize", "/v1/load/finalize", "callback.info.start", "callback.event.start", "callback.display.start", "callback.preview.start", "callback.load.start", "event_id", "applied", "idempotent", "callback application failed", "64 KiB", "64 MiB", "1 KiB", "4 MiB total output", "1,000,000 rows", "128 KiB per row"} {
		requireDocumented(t, doc, value)
	}
	for _, source := range []string{"internal/sessionipc/server.go", "internal/sessionipc/client.go"} {
		data, err := os.ReadFile(filepath.Join("..", source))
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []string{"64 << 10", "64 << 20", "1 << 10"} {
			if strings.Contains(string(data), limit) && !strings.Contains(doc, map[string]string{"64 << 10": "64 KiB", "64 << 20": "64 MiB", "1 << 10": "1 KiB"}[limit]) {
				t.Errorf("%s limit %s is undocumented", source, limit)
			}
		}
	}
	if got := candidate.DefaultZoxideTimeout().String(); !strings.Contains(readDoc(t, "README.md"), got) {
		t.Fatalf("README does not document default zoxide timeout %s", got)
	}
	if integration.TraceSchema != 2 || !strings.Contains(doc, "schema 2") {
		t.Fatal("trace schema documentation is not synchronized")
	}
}

func TestDocumentedTraceFieldsMatchSchema(t *testing.T) {
	doc := readDoc(t, "docs/protocol.md")
	typeOf := reflect.TypeOf(integration.TraceRecord{})
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "" {
			requireDocumented(t, doc, "`"+name+"`")
		}
	}
}

func TestDocumentationParityAuthority(t *testing.T) {
	data, err := os.ReadFile("testdata/parity/source-assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Sources    map[string]string `json:"sources"`
		Assertions []struct {
			Suite string `json:"suite"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Assertions) != 371 || len(manifest.Sources) != 5 {
		t.Fatalf("manifest has %d assertions and %d sources", len(manifest.Assertions), len(manifest.Sources))
	}
	doc := readDoc(t, "docs/parity.md")
	counts := map[string]int{}
	for _, row := range manifest.Assertions {
		counts[row.Suite]++
	}
	for source, hash := range manifest.Sources {
		requireDocumented(t, doc, source)
		requireDocumented(t, doc, hash)
	}
	for suite, count := range counts {
		requireDocumented(t, doc, fmt.Sprintf("%s: %d", suite, count))
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "611cfc8ba119df12189d5b61e9c634730c5e1c8952526e9525ecff03a7669d45" {
		t.Fatalf("manifest hash %s", got)
	}
}

func TestDocumentationPathsLicenseAndExamples(t *testing.T) {
	for _, document := range markdownDocuments(t) {
		validateDocumentedPaths(t, document)
	}
	license := readDoc(t, "LICENSE")
	want, err := os.ReadFile(filepath.Join("testdata", "mit-license.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if license != string(want) {
		t.Fatal("LICENSE is not the exact MIT text")
	}
}

func validateDocumentedPaths(t *testing.T, document string) {
	t.Helper()
	plain := markdownOutsideFences(readDoc(t, document))
	for _, match := range regexp.MustCompile(`!?\[[^]]*\]\(([^)[:space:]]+)`).FindAllStringSubmatch(plain, -1) {
		validateRepositoryPath(t, document, match[1], true)
	}
	for _, match := range regexp.MustCompile("`([^`\\n]+)`").FindAllStringSubmatch(plain, -1) {
		validateRepositoryPath(t, document, match[1], false)
	}
}

func validateRepositoryPath(t *testing.T, document, raw string, link bool) {
	t.Helper()
	raw = strings.Trim(raw, "<>.,;:()[]{}")
	raw, _, _ = strings.Cut(raw, "#")
	if raw == "" || strings.Contains(raw, "://") || strings.HasPrefix(raw, "#") || filepath.IsAbs(raw) {
		return
	}
	repoRoot := filepath.Clean("..")
	documentRelative := filepath.Join(repoRoot, filepath.Dir(document), filepath.FromSlash(raw))
	repoRelative := filepath.Join(repoRoot, filepath.FromSlash(raw))
	if link {
		if _, err := os.Stat(documentRelative); err != nil {
			t.Errorf("%s links to missing repository path %q: %v", document, raw, err)
		}
		return
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return
	}
	if _, err := os.Stat(documentRelative); err == nil {
		return
	}
	if _, err := os.Stat(repoRelative); err == nil {
		return
	}
	first, _, hasSlash := strings.Cut(filepath.ToSlash(raw), "/")
	if !hasSlash || !topLevelDirectory(first) || !looksLikePath(raw) {
		return
	}
	if _, err := os.Stat(repoRelative); err != nil {
		t.Errorf("%s documents missing repository path %q: %v", document, raw, err)
	}
}

func topLevelDirectory(name string) bool {
	info, err := os.Stat(filepath.Join("..", name))
	return err == nil && info.IsDir()
}

func looksLikePath(value string) bool {
	extension := strings.ToLower(filepath.Ext(value))
	if extension == "" {
		return true
	}
	for _, accepted := range []string{".go", ".json", ".md", ".nu", ".sh", ".toml", ".yaml", ".yml", ".zsh"} {
		if extension == accepted {
			return true
		}
	}
	return false
}

func markdownOutsideFences(document string) string {
	lines := strings.Split(document, "\n")
	inside := false
	for index, line := range lines {
		if strings.HasPrefix(line, "```") {
			inside = !inside
			lines[index] = ""
			continue
		}
		if inside {
			lines[index] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func requireDocumented(t *testing.T, document, value string) {
	t.Helper()
	if !strings.Contains(document, value) {
		t.Errorf("missing documented contract %q", value)
	}
}
