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
	for _, opcode := range []protocol.Opcode{protocol.OpModeInsert, protocol.OpModeAdd, protocol.OpEscape, protocol.OpForward, protocol.OpParent, protocol.OpSlash, protocol.OpHome, protocol.OpEnter} {
		requireDocumented(t, doc, "e:"+string(opcode))
	}
	for _, value := range []string{protocol.VirtualDrivesTarget, protocol.EncodePath([]byte(protocol.VirtualDrivesTarget)), "p", "l:<positive decimal generation>", "64 KiB", "64 MiB", "1 KiB"} {
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
	if integration.TraceSchema != 1 || !strings.Contains(doc, "schema 1") {
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
	for _, event := range []string{"session.start", "generation.start", "generation.publish", "generation.discard", "fzf.start", "fzf.exit", "callback.event", "callback.load", "preview.dispatch", "preview.finished", "preview.cancel", "preview.exit", "session.close"} {
		requireDocumented(t, doc, event)
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
	for _, path := range []string{"LICENSE", "README.md", "docs/architecture.md", "docs/protocol.md", "docs/adapters.md", "docs/preview.md", "docs/performance.md", "docs/parity.md", "docs/security.md", "adapters/zsh/shell-picker.plugin.zsh", "adapters/nushell/shell-picker.nu"} {
		if _, err := os.Stat(filepath.Join("..", path)); err != nil {
			t.Errorf("documented path %s: %v", path, err)
		}
	}
	license := readDoc(t, "LICENSE")
	want, err := os.ReadFile(filepath.Join("testdata", "mit-license.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if license != string(want) {
		t.Fatal("LICENSE is not the exact MIT text")
	}
	for _, path := range []string{"README.md", "docs/architecture.md", "docs/protocol.md", "docs/adapters.md", "docs/preview.md", "docs/performance.md", "docs/parity.md", "docs/security.md"} {
		for _, header := range regexp.MustCompile("(?m)^```(?:sh|bash)(.*)$").FindAllStringSubmatch(readDoc(t, path), -1) {
			if !strings.Contains(header[1], "check") && !strings.Contains(header[1], "example") {
				t.Errorf("%s has unclassified shell example", path)
			}
		}
	}
}

func requireDocumented(t *testing.T, document, value string) {
	t.Helper()
	if !strings.Contains(document, value) {
		t.Errorf("missing documented contract %q", value)
	}
}
