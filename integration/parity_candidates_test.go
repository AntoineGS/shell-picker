package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func parityCaseBytes(name string) []byte {
	switch name {
	case "tab-name":
		return []byte("tab\tname")
	case "line-newline-name":
		return []byte("line\nname")
	case "trailing-space":
		return []byte("trailing ")
	case "backslash-name":
		return []byte(`back\slash`)
	case "nbsp-name":
		return []byte("nbsp\u00a0name")
	case "leading-dash":
		return []byte("-leading")
	case "control-byte-name":
		return []byte("control\x01name")
	case "ending-newline":
		return []byte("ending-newline\n")
	case "apostrophe-path":
		return []byte("apostrophe'path")
	default:
		return nil
	}
}

func runCodecRoundtrip(t *testing.T, row parityRow) {
	if row.Check == "decode-rejected" {
		_, err := protocol.DecodePath("not/base64%")
		assertParityText(t, row, boolText(err != nil))
		return
	}
	input := parityCaseBytes(row.Case)
	decoded, err := protocol.DecodePath(protocol.EncodePath(input))
	assertParityText(t, row, boolText(err == nil && bytes.Equal(decoded, input)))
}

func runCodecRecord(t *testing.T, row parityRow) {
	records := loadWireGolden(t, "codec-records.bin")
	if row.Check == "record-count" {
		assertParityText(t, row, strconv.Itoa(len(records)))
		return
	}
	wire := codecGoldenRecord(t, records, row.Case)
	switch row.Check {
	case "record-tab-count":
		assertParityText(t, row, strconv.Itoa(bytes.Count(wire.Bytes(), []byte{'\t'})))
	case "kind":
		assertParityText(t, row, string(wire.Kind))
	case "display-excludes-octal-escape":
		assertParityText(t, row, boolText(!strings.Contains(wire.Display, `\001`)))
	case "escaped-display":
		assertParityText(t, row, wire.Display)
	case "payload-bytes-equal-input":
		decoded, err := protocol.DecodePath(wire.Payload)
		want := parityCaseBytes(row.Case)
		assertParityText(t, row, boolText(err == nil && bytes.HasSuffix(decoded, want)))
	default:
		t.Fatalf("unhandled codec check %q", row.Check)
	}
}

func codecGoldenRecord(t *testing.T, records []protocol.WireRecord, name string) protocol.WireRecord {
	t.Helper()
	display := protocol.EscapeDisplay(parityCaseBytes(name))
	if name == "current-directory" {
		display = "."
	} else if name == "parent-directory" {
		display = ".."
	}
	for _, record := range records {
		if record.Display == display {
			return record
		}
	}
	t.Fatalf("codec golden has no case %q (%q)", name, display)
	return protocol.WireRecord{}
}

func loadWireGolden(t *testing.T, name string) []protocol.WireRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "parity", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		t.Fatalf("golden %s is not NUL framed", name)
	}
	frames := bytes.Split(raw[:len(raw)-1], []byte{0})
	records := make([]protocol.WireRecord, len(frames))
	for index, frame := range frames {
		records[index], err = protocol.ParseRecord(frame)
		if err != nil {
			t.Fatalf("parse %s record %d: %v", name, index, err)
		}
	}
	return records
}

type parityFailWriter struct{ writes int }

func (writer *parityFailWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == 2 {
		return 0, errors.New("injected writer failure")
	}
	return len(data), nil
}

func runBatchEncoder(t *testing.T, row parityRow) {
	lines := [][]byte{
		[]byte("plain path"), []byte("tab\tname"), []byte(`back\slash`), []byte("control\x01name"),
		[]byte("trailing "), []byte("caf\u00e9"), append([]byte("invalid-"), 0xff),
	}
	if row.Check == "exit-failure-propagated" {
		writer := new(parityFailWriter)
		err := protocol.WriteFramedRecords(writer, []protocol.WireRecord{{Kind: protocol.KindFile, Display: "one", Payload: protocol.EncodePath(lines[0])}})
		assertParityText(t, row, boolText(err != nil && writer.writes == 2))
		return
	}
	var got []byte
	for _, line := range lines {
		got = append(got, protocol.EncodePath(line)...)
		got = append(got, 0)
		got = append(got, line...)
		got = append(got, 0)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "parity", "golden", "batch-encoder.bin"))
	if err != nil {
		t.Fatal(err)
	}
	assertParityText(t, row, boolText(bytes.Equal(got, want)))
}

type parityDirectoryName struct {
	caseName string
	name     string
}

func runEnumeration(t *testing.T, row parityRow) {
	root := t.TempDir()
	if row.Case == "cd-local-ignored-worktrees" {
		if err := os.Mkdir(filepath.Join(root, ".worktrees"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, entry := range platformParityDirectoryNames() {
			if err := os.Mkdir(filepath.Join(root, entry.name), 0o755); err != nil {
				t.Fatalf("create %q: %v", entry.name, err)
			}
		}
	}
	picker := protocol.PickerCD
	if strings.HasPrefix(row.Case, "cp-") || row.Case == "cp" {
		picker = protocol.PickerCP
	}
	records, err := candidate.EnumerateLocal(context.Background(), picker, pathutil.Filesystem([]byte(root)), candidate.LocalOptions{StatWorkers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if row.Check == "record-count" {
		assertParityText(t, row, strconv.Itoa(len(records)))
		return
	}
	record := parityEnumerationRecord(t, records, row.Case)
	switch row.Check {
	case "record-tab-count":
		assertParityText(t, row, strconv.Itoa(bytes.Count(record.Wire().Bytes(), []byte{'\t'})))
	case "kind":
		assertParityText(t, row, string(record.Kind))
	case "escaped-display", "display":
		assertParityText(t, row, record.Display)
	case "payload-bytes-equal-input":
		decoded, decodeErr := protocol.DecodePath(record.Payload)
		assertParityText(t, row, boolText(decodeErr == nil && bytes.Equal(decoded, record.Path)))
	default:
		t.Fatalf("unhandled enumeration check %q", row.Check)
	}
}

func parityEnumerationRecord(t *testing.T, records []candidate.Record, caseName string) candidate.Record {
	t.Helper()
	if strings.HasSuffix(caseName, "current-directory") {
		return findParityRecord(t, records, ".")
	}
	if strings.HasSuffix(caseName, "parent-directory") {
		return findParityRecord(t, records, "..")
	}
	if strings.HasSuffix(caseName, "ignored-worktrees") {
		return findParityRecord(t, records, ".worktrees")
	}
	for _, entry := range platformParityDirectoryNames() {
		if strings.Contains(caseName, entry.caseName) {
			display := protocol.EscapeDisplay([]byte(entry.name))
			if strings.HasPrefix(caseName, "cp-") {
				display += string(filepath.Separator)
			}
			return findParityRecord(t, records, display)
		}
	}
	t.Fatalf("no enumeration fixture for %q", caseName)
	return candidate.Record{}
}

func findParityRecord(t *testing.T, records []candidate.Record, display string) candidate.Record {
	t.Helper()
	for _, record := range records {
		if record.Display == display {
			return record
		}
	}
	t.Fatalf("record %q absent from %v", display, parityDisplays(records))
	return candidate.Record{}
}

func parityDisplays(records []candidate.Record) []string {
	result := make([]string, len(records))
	for index, record := range records {
		result[index] = record.Display
	}
	return result
}

func runMerge(t *testing.T, row parityRow) {
	root := t.TempDir()
	for _, name := range []string{".hidden", "visible"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mode := "zoxide-ok"
	path := paritySelfExecutable(t)
	switch row.Case {
	case "zoxide-exit-failure", "encoder-exit-failure":
		mode = "zoxide-fail"
	case "zoxide-partial-failure", "encoder-partial-failure":
		mode = "zoxide-malformed"
	case "encoder-missing":
		path = filepath.Join(root, "missing-zoxide")
	}
	environment := replaceEnvironment(os.Environ(), parityHelperEnvironment+"="+mode, "PARITY_TEST_ROOT="+root)
	cache, err := candidate.NewZoxideCache(processpkg.Runner{}, path, environment, 0)
	if err != nil {
		t.Fatal(err)
	}
	builder := candidate.Builder{}
	builder.ConfigureCached(cache)
	result, err := builder.Build(context.Background(), candidate.BuildRequest{
		Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte(root)), Initial: true, StatWorkers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Check == "external-candidates-discarded" {
		assertParityText(t, row, boolText(result.ZoxideDiscarded && len(result.Records) == 4))
		return
	}
	want := loadWireGolden(t, "cd-order.bin")
	switch row.Check {
	case "decoded-path-order":
		gotNames := make([]string, len(result.Records))
		for index, record := range result.Records {
			gotNames[index] = filepath.Base(string(record.Path))
		}
		wantNames := []string{filepath.Base(root), filepath.Base(filepath.Dir(root)), ".hidden", "visible", "zoxide-one", "zoxide-two"}
		assertParityText(t, row, boolText(reflect.DeepEqual(gotNames, wantNames)))
	case "kind-order":
		kinds := make([]string, len(result.Records))
		for index, record := range result.Records {
			kinds[index] = string(record.Kind)
		}
		assertParityText(t, row, strings.Join(kinds, ","))
	case "display-order":
		goldenDisplays := make([]string, len(want))
		for index, record := range want {
			goldenDisplays[index] = record.Display
			if index >= 4 {
				goldenDisplays[index] = filepath.Base(goldenDisplays[index])
			}
		}
		actualDisplays := parityDisplays(result.Records)
		for index := 4; index < len(actualDisplays); index++ {
			actualDisplays[index] = filepath.Base(actualDisplays[index])
		}
		assertParityText(t, row, strings.Join(actualDisplays, ","))
		if strings.Join(goldenDisplays, ",") != row.ExpectedText {
			t.Fatalf("cd golden display order=%q, row=%q", strings.Join(goldenDisplays, ","), row.ExpectedText)
		}
	default:
		t.Fatalf("unhandled merge check %q", row.Check)
	}
}

func paritySelfExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
