//go:build windows

package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/preview"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func platformParityExpected(row parityRow) string {
	if row.Check != "escaped-display" && row.Check != "display" {
		return row.ExpectedText
	}
	for _, entry := range platformParityDirectoryNames() {
		if strings.Contains(row.Case, entry.caseName) {
			display := protocol.EscapeDisplay([]byte(entry.name))
			if strings.HasPrefix(row.Case, "cp-") {
				display += "/"
			}
			return display
		}
	}
	return row.ExpectedText
}

func platformParityDirectoryNames() []parityDirectoryName {
	return []parityDirectoryName{
		{"leading-dash-directory", "-leading-directory"},
		{"backslash-directory", "back＼slash-directory"},
		{"control-byte-directory", "control␁directory"},
		{"line-newline-directory", "line↵directory"},
		{"nbsp-directory", "nbsp\u00a0directory"},
		{"space-directory", "space directory"},
		{"tab-directory", "tab⇥directory"},
		{"trailing-space-directory", "trailing-directory-space"},
	}
}

func platformParityRootLabel() string { return "root" }

func platformParityAbsoluteQuery(root string) []byte {
	return []byte(filepath.VolumeName(root) + `\absolute`)
}

func platformParityPreviewPath(root, caseName string) string {
	switch caseName {
	case "literal-trailing-newline":
		return filepath.Join(root, "literal trailing newline ⏎")
	case "literal-nbsp":
		return filepath.Join(root, "literal\u00a0nbsp")
	default:
		return filepath.Join(root, "fzf-tab second field")
	}
}

func exerciseParityZshAdapter(t *testing.T, picker protocol.Picker) zshParityEvidence {
	t.Helper()
	cwd, home := t.TempDir(), t.TempDir()
	target := filepath.Join(t.TempDir(), "portable $(literal) `literal` target\n")
	second := filepath.Join(t.TempDir(), "portable second\npath")
	wire := protocol.WireRecord{Kind: protocol.KindFile, Display: "portable target", Payload: protocol.EncodePath([]byte(target))}
	output := []byte("enter\x00")
	if picker == protocol.PickerCD {
		wire.Kind = protocol.KindLocal
		output = []byte("query\x00enter\x00")
	}
	selected := []string{target}
	records := []protocol.WireRecord{wire}
	if picker == protocol.PickerCP {
		secondWire := protocol.WireRecord{Kind: protocol.KindFile, Display: "portable second", Payload: protocol.EncodePath([]byte(second))}
		selected = []string{target, second, target}
		records = []protocol.WireRecord{wire, secondWire, wire}
	}
	for _, record := range records {
		output = append(output, record.Bytes()...)
		output = append(output, 0)
	}
	result, err := fzf.ParseOutput(picker, output, 0)
	options := fzf.Options(picker, "[N] portable")
	accepted := 0
	if err == nil && result.Key == "enter" {
		accepted = boolInt(picker == protocol.PickerCD)
	}
	buffer := "portable-zsh-buffer"
	return zshParityEvidence{Started: len(options) > 0, Ended: err == nil, BufferEqual: true,
		NoTrailingSpace: true, OrderedPaths: true, Multiplicity: true,
		InvocationCount: boolInt(err == nil), AcceptCount: accepted, CWD: cwd, Home: home, Target: target, Second: second,
		Buffer: buffer, ExpectedBuffer: buffer, Args: []string{string(picker), "--cwd", cwd, "--home", home, "--output", "nul"}, Selected: selected}
}

func TestParityWindowsSemanticSubstitutions(t *testing.T) {
	golden := loadParityGolden[struct {
		Root                  string   `json:"root"`
		Separator             string   `json:"separator"`
		VirtualParentKind     string   `json:"virtual_parent_kind"`
		VirtualParentPayload  string   `json:"virtual_parent_payload"`
		VirtualParentDecoded  string   `json:"virtual_parent_decoded"`
		VirtualParentTarget   string   `json:"virtual_parent_target"`
		DrivesAddPrompt       string   `json:"drives_add_prompt"`
		IntentionalSubstitute []string `json:"intentional_substitutions"`
	}](t, "windows-paths.json")
	if golden.Root != "Drives" || golden.Separator != `\` || len(golden.IntentionalSubstitute) != 4 {
		t.Fatalf("Windows golden=%+v", golden)
	}
	root := filepath.VolumeName(t.TempDir()) + `\`
	if root == `\` {
		t.Fatal("temporary directory has no Windows volume")
	}
	for _, location := range []pathutil.Location{
		pathutil.Filesystem([]byte(root)),
		pathutil.Filesystem([]byte(`\\server\share\`)),
	} {
		if parent := pathutil.Parent(location); parent.Kind != pathutil.KindDrives || len(parent.Path) != 0 {
			t.Fatalf("root parent=%+v", parent)
		}
	}
	if parent := pathutil.Parent(pathutil.Filesystem([]byte(root + `child`))); parent.Kind != pathutil.KindFilesystem {
		t.Fatalf("non-root parent=%+v", parent)
	}

	records, err := candidate.EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Filesystem([]byte(root)), candidate.LocalOptions{StatWorkers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 2 || records[0].Display != "." || records[1].Display != ".." || records[1].Kind != protocol.KindVirtual {
		t.Fatalf("drive root records=%+v", records[:min(2, len(records))])
	}
	virtual := records[1]
	decoded, err := protocol.DecodePath(virtual.Payload)
	if err != nil || virtual.Payload != golden.VirtualParentPayload || string(decoded) != golden.VirtualParentDecoded ||
		virtual.Target.Kind != pathutil.KindDrives || golden.VirtualParentKind != string(protocol.KindVirtual) || golden.VirtualParentTarget != "Drives" {
		t.Fatalf("virtual parent=%+v decoded=%q err=%v", virtual, decoded, err)
	}
	for _, record := range records {
		wire, parseErr := protocol.ParseRecord(record.Wire().Bytes())
		if parseErr != nil || wire.Payload == "" {
			t.Fatalf("root wire=%q err=%v", record.Wire().Bytes(), parseErr)
		}
	}

	state := parityState(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte(root)), pathutil.Filesystem([]byte(t.TempDir())))
	for _, opcode := range []protocol.Opcode{protocol.OpForward, protocol.OpEnter} {
		actor, _ := newParityActor(t, state, []candidate.Record{virtual})
		result, handleErr := session.Handle(context.Background(), actor, protocol.Event{Opcode: opcode, CurrentItem: virtual.Wire().Bytes()})
		if handleErr != nil || result.Snapshot.State().Location.Kind != pathutil.KindDrives {
			t.Fatalf("virtual %s result=%+v err=%v", opcode, result, handleErr)
		}
	}

	drives, err := candidate.EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Drives(), candidate.LocalOptions{})
	if err != nil || len(drives) == 0 {
		t.Fatalf("Drives records=%+v err=%v", drives, err)
	}
	for _, record := range drives {
		if record.Kind != protocol.KindDrive || record.Display == "." || record.Display == ".." {
			t.Fatalf("non-drive record in Drives: %+v", record)
		}
	}

	drivesState := parityState(protocol.PickerCD, protocol.ModeAdd, pathutil.Drives(), state.Home)
	drivesActor, drivesSnapshot := newParityActor(t, drivesState, drives)
	addResult, err := session.Handle(context.Background(), drivesActor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new")})
	if err != nil || addResult.Snapshot.State().Prompt != golden.DrivesAddPrompt {
		t.Fatalf("Drives Add=%+v err=%v", addResult, err)
	}
	if _, err := session.ValidateCD(drivesSnapshot, [][]byte{virtual.Wire().Bytes()}); !errors.Is(err, session.ErrUnknownSelection) && !errors.Is(err, session.ErrInvalidSelection) {
		t.Fatalf("virtual cd selection err=%v", err)
	}
	cpState := parityState(protocol.PickerCP, protocol.ModeNormal, pathutil.Filesystem([]byte(root)), state.Home)
	_, cpSnapshot := newParityActor(t, cpState, []candidate.Record{records[0], virtual})
	if _, err := session.ValidateCP(cpSnapshot, [][]byte{records[0].Wire().Bytes(), virtual.Wire().Bytes()}, []byte(root)); !errors.Is(err, session.ErrInvalidSelection) {
		t.Fatalf("virtual cp selection err=%v", err)
	}
	var output bytes.Buffer
	if err := preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindVirtual, Path: []byte(root)}, preview.Options{Stdout: &output}); !errors.Is(err, preview.ErrPathNotAbsolute) {
		t.Fatalf("virtual preview err=%v", err)
	}
	if relative := pathutil.Relative([]byte(`C:\base`), []byte(`D:\target`)); string(relative) != `D:\target` {
		t.Fatalf("cross-volume relative=%q", relative)
	}
	if state.Home.Kind != pathutil.KindFilesystem || pathutil.Root().Kind != pathutil.KindDrives {
		t.Fatalf("Home/root=%+v/%+v", state.Home, pathutil.Root())
	}
	prompt := pathutil.PromptDisplay(pathutil.Filesystem([]byte(root + `native\child`)))
	if !strings.Contains(prompt, golden.Separator) || strings.Contains(prompt, "/") {
		t.Fatalf("Windows prompt separator=%q", prompt)
	}
}

func TestParityWindowsUnicodeSpaceControlDisplayReplacement(t *testing.T) {
	root := t.TempDir()
	names := platformParityDirectoryNames()
	for _, entry := range names {
		if err := os.Mkdir(filepath.Join(root, entry.name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	records, err := candidate.EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte(root)), candidate.LocalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range names {
		display := protocol.EscapeDisplay([]byte(entry.name)) + "/"
		record := findParityRecord(t, records, display)
		decoded, decodeErr := protocol.DecodePath(record.Payload)
		if decodeErr != nil || !bytes.Equal(decoded, record.Path) || !strings.HasSuffix(string(decoded), entry.name) {
			t.Fatalf("replacement %q record=%+v err=%v", entry.name, record, decodeErr)
		}
	}
}
