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
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

type zshAdapterGolden struct {
	CD struct {
		Multi      string `json:"multi"`
		Sort       string `json:"sort"`
		PrintQuery string `json:"print_query"`
	} `json:"cd"`
	CP struct {
		Multi      string `json:"multi"`
		Sort       string `json:"sort"`
		Terminator string `json:"terminator"`
	} `json:"cp"`
	Bindings         []string `json:"bindings"`
	IntentionalFixes []string `json:"intentional_fixes"`
}

type zshParityEvidence struct {
	Started, Ended, BufferEqual, NoTrailingSpace, OrderedPaths, Multiplicity bool
	InvocationCount, AcceptCount                                             int
	CWD, Home, Target, Second, Buffer, ExpectedBuffer                        string
	Args, Selected                                                           []string
}

func TestParityZshSemanticRows(t *testing.T) {
	count := 0
	for _, row := range loadParityMatrix(t) {
		if row.Suite != "zshrc-cd" && row.Suite != "zshrc-cp" {
			continue
		}
		row := row
		t.Run(row.ID, func(t *testing.T) { runParityRow(t, row) })
		count++
	}
	if count != 85 {
		t.Fatalf("focused Zsh semantic rows=%d, want 85", count)
	}
}

func runZshSemantic(t *testing.T, row parityRow, picker protocol.Picker) {
	golden := loadParityGolden[zshAdapterGolden](t, "zsh-adapter.json")
	evidence := exerciseParityZshAdapter(t, picker)
	root := evidence.CWD
	first := newParityRecord(protocol.KindFile, "first display", []byte(evidence.Target))
	second := newParityRecord(protocol.KindFile, "second display", []byte(evidence.Second))
	if picker == protocol.PickerCD {
		first.Kind = protocol.KindLocal
		first.Target = pathutil.Filesystem(first.Path)
	} else {
		first.Kind = protocol.KindDirectory
	}
	state := parityState(picker, protocol.ModeNormal, pathutil.Filesystem([]byte(root)), pathutil.Filesystem([]byte(evidence.Home)))
	actor, snapshot := newParityActor(t, state, []candidate.Record{first, second, first})
	options := fzf.Options(picker, state.Prompt, pathutil.PromptDisplay(state.Location))
	joined := strings.Join(options, "\n")
	for _, key := range golden.Bindings {
		needle := "--bind=" + key + ":"
		if !strings.Contains(joined, needle) {
			t.Fatalf("golden binding %q absent from %s options", key, picker)
		}
	}
	if picker == protocol.PickerCD {
		if !containsString(options, golden.CD.Multi) || !containsString(options, golden.CD.Sort) || !containsString(options, golden.CD.PrintQuery) {
			t.Fatalf("cd options do not satisfy golden: %q", options)
		}
	} else if !containsString(options, golden.CP.Multi) || !containsString(options, golden.CP.Sort) || golden.CP.Terminator != "--" {
		t.Fatalf("cp options do not satisfy golden: %q", options)
	}

	switch row.Check {
	case "start-present":
		assertParityText(t, row, boolText(evidence.Started))
	case "end-after-start":
		assertParityText(t, row, boolText(evidence.Started && evidence.Ended))
	case "encoded-cwd":
		assertParityText(t, row, boolText(zshArgValue(evidence.Args, "--cwd") == evidence.CWD))
	case "encoded-home":
		assertParityText(t, row, boolText(zshArgValue(evidence.Args, "--home") == evidence.Home))
	case "encoded-base":
		assertParityText(t, row, boolText(zshArgValue(evidence.Args, "--cwd") == evidence.CWD &&
			bytes.Equal(snapshot.State().Location.Path, []byte(evidence.CWD))))
	case "encoded-root":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpSlash})
		assertParityText(t, row, boolText(err == nil && sameParityLocation(result.Snapshot.State().Location, pathutil.Root()) &&
			bytes.Equal(snapshot.State().Location.Path, []byte(evidence.CWD))))
	case "escape-callback":
		assertParityText(t, row, boolText(strings.Contains(joined, "transform(e:"+string(protocol.OpEscape)+")")))
	case "enter-callback":
		assertParityText(t, row, boolText(strings.Contains(joined, "transform(e:"+string(protocol.OpEnter)+")")))
	case "slash-callback":
		assertParityText(t, row, boolText(strings.Contains(joined, "transform(e:"+string(protocol.OpSlash)+")")))
	case "add-mode-callback":
		assertParityText(t, row, boolText(strings.Contains(joined, "transform(e:"+string(protocol.OpModeAdd)+")")))
	case "normal-keys-unbound":
		assertParityText(t, row, boolText(strings.Contains(joined, "--bind=start:unbind(h,j,k,l,i,a,q,space)")))
	case "initialized", "feeds-fzf":
		assertParityText(t, row, boolText(len(options) > 20 && strings.Contains(joined, "--read0") && strings.Contains(joined, "--print0")))
	case "excludes-cd-local":
		assertParityText(t, row, boolText(!strings.Contains(joined, "cd-local")))
	case "excludes-cd-zoxide":
		assertParityText(t, row, boolText(!strings.Contains(joined, "cd-zoxide")))
	case "excludes-source-mode-state":
		assertParityText(t, row, boolText(!strings.Contains(joined, "source-mode")))
	case "excludes-shift-tab-switching", "shift-tab-source-switching-excluded":
		assertParityText(t, row, boolText(!strings.Contains(joined, "shift-tab")))
	case "excludes-toggle-sort":
		assertParityText(t, row, boolText(!strings.Contains(joined, "toggle-sort")))
	case "occurrences":
		count := boolInt(strings.Contains(joined, "--delimiter=\t")) + boolInt(strings.Contains(joined, "--with-nth=2"))
		assertParityText(t, row, strconv.Itoa(count))
	case "forward-assignment-occurrences":
		assertParityText(t, row, strconv.Itoa(strings.Count(joined, "ctrl-l,tab,right")))
	case "forward-and-parent-route-count":
		count := boolInt(strings.Contains(joined, string(protocol.OpForward))) + boolInt(strings.Contains(joined, string(protocol.OpParent)))
		assertParityText(t, row, strconv.Itoa(count))
	case "parent-helper-route", "parent-starts-current-directory":
		parent := pathutil.Parent(pathutil.Filesystem([]byte(root)))
		assertParityText(t, row, boolText(parent.Kind == pathutil.KindFilesystem && string(parent.Path) == filepath.Dir(root)))
	case "encoded-helper-route", "field-three-helper-route":
		resolved, err := actor.ResolveCurrent(context.Background(), first.Wire().Bytes())
		assertParityText(t, row, boolText(err == nil && bytes.Equal(resolved.Path, []byte(evidence.Target)) &&
			resolved.FullKey() == first.FullKey()))
	case "field-three-parsed":
		assertParityText(t, row, boolText(validateZshCDSelection(t, snapshot, first)))
	case "payload-decoded":
		assertParityText(t, row, boolText(validateZshCDSelection(t, snapshot, first)))
	case "nul-aware-read":
		assertParityText(t, row, boolText(validateZshCDSelection(t, snapshot, first) && len(evidence.Selected) == 1 &&
			evidence.Selected[0] == evidence.Target && strings.Contains(evidence.Target, "\n")))
	case "no-command-substitution-decode":
		assertParityText(t, row, boolText(validateZshCDSelection(t, snapshot, first) && evidence.BufferEqual &&
			strings.Contains(evidence.Target, "$(") && strings.Contains(evidence.Target, "`")))
	case "zsh-quoted-command":
		assertParityText(t, row, boolText(validateZshCDSelection(t, snapshot, first) && evidence.BufferEqual &&
			evidence.Buffer == evidence.ExpectedBuffer))
	case "exactly-one-target":
		valid := validateZshCDSelection(t, snapshot, first) && len(evidence.Selected) == 1 && evidence.AcceptCount == 1
		assertParityText(t, row, boolText(valid))
	case "destination-count":
		assertParityText(t, row, strconv.Itoa(zshNavigationDestinationCount(t, picker, state, first)))
	case "root-does-not-bypass-slash":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpSlash})
		assertParityText(t, row, boolText(err == nil && sameParityLocation(result.Snapshot.State().Location, pathutil.Root())))
	case "encoded-home-route":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpHome})
		assertParityText(t, row, boolText(err == nil && sameParityLocation(result.Snapshot.State().Location, state.Home) &&
			zshArgValue(evidence.Args, "--home") == evidence.Home))
	case "raw-root-excluded", "raw-home-excluded", "raw-path-modifier-excluded", "encoded-eza-route-excluded", "encoded-script-route-excluded", "direct-reload-excluded":
		if row.Check == "raw-root-excluded" {
			result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpSlash})
			assertParityText(t, row, boolText(err == nil && sameParityLocation(result.Snapshot.State().Location, pathutil.Root()) &&
				strings.Contains(joined, "transform(e:")))
			break
		}
		if row.Check == "raw-home-excluded" {
			result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpHome})
			assertParityText(t, row, boolText(err == nil && bytes.Equal(result.Snapshot.State().Location.Path, []byte(evidence.Home)) &&
				zshArgValue(evidence.Args, "--home") == evidence.Home))
			break
		}
		forbidden := map[string]string{
			"raw-path-modifier-excluded": ":a}",
			"encoded-eza-route-excluded": "eza", "encoded-script-route-excluded": "fzf-preview.sh", "direct-reload-excluded": "reload(",
		}[row.Check]
		assertParityText(t, row, boolText(!strings.Contains(joined, forbidden)))
	case "single":
		assertParityText(t, row, boolText(containsString(options, golden.CD.Multi)))
	case "sort-and-query":
		assertParityText(t, row, boolText(containsString(options, golden.CD.Sort) && containsString(options, golden.CD.PrintQuery)))
	case "fzf-count":
		assertParityText(t, row, strconv.Itoa(evidence.InvocationCount))
	case "buffer":
		got := ""
		if evidence.BufferEqual && evidence.Buffer == evidence.ExpectedBuffer && strings.HasSuffix(evidence.Target, "\n") {
			got = "zsh-quoted-decoded-target"
		}
		assertParityText(t, row, got)
	case "accept-line-count":
		assertParityText(t, row, strconv.Itoa(evidence.AcceptCount))
	case "unrestricted-multi":
		assertParityText(t, row, boolText(containsString(options, golden.CP.Multi)))
	case "single-selection-excluded":
		assertParityText(t, row, boolText(!containsString(options, "--multi=1")))
	case "normal-space-toggle":
		assertParityText(t, row, boolText(strings.Contains(joined, "--bind=space:toggle")))
	case "normal-space-preserves-marks":
		assertParityText(t, row, boolText(!strings.Contains(joined, "--bind=space:clear-multi")))
	case "enter-key-first":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && len(evidence.Selected) == 3 &&
			evidence.Selected[0] == evidence.Target))
	case "all-records-collected":
		assertParityText(t, row, boolText(exactZshCPSelections(evidence)))
	case "exactly-two-tabs":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) &&
			bytes.Count(first.Wire().Bytes(), []byte{'\t'}) == 2 && bytes.Count(second.Wire().Bytes(), []byte{'\t'}) == 2))
	case "full-record-count-map":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && len(snapshot.Records()) == 3 &&
			exactZshCPSelections(evidence)))
	case "counts-keyed-by-full-record":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && first.FullKey() != second.FullKey() &&
			exactZshCPSelections(evidence)))
	case "nested-matching-excluded":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && exactZshCPSelections(evidence)))
	case "all-records-match":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && exactZshCPSelections(evidence)))
	case "all-payloads-collected":
		assertParityText(t, row, boolText(exactZshCPSelections(evidence)))
	case "atomic-relative-decode":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && exactZshCPSelections(evidence)))
	case "nul-aware-relative-read":
		assertParityText(t, row, boolText(exactZshCPSelections(evidence) && strings.Contains(evidence.Second, "\n")))
	case "decode-cardinality":
		assertParityText(t, row, boolText(validateZshCPSelection(t, snapshot, root, first, second) && len(evidence.Selected) == 3 &&
			exactZshCPSelections(evidence)))
	case "zsh-quote-each-path":
		assertParityText(t, row, boolText(evidence.BufferEqual && evidence.Buffer == evidence.ExpectedBuffer && exactZshCPSelections(evidence)))
	case "single-space-join":
		assertParityText(t, row, boolText(evidence.BufferEqual && evidence.Buffer == evidence.ExpectedBuffer && evidence.OrderedPaths &&
			evidence.NoTrailingSpace && exactZshCPSelections(evidence)))
	case "restored-by-full-record":
		goldenRecords := loadWireGolden(t, "cp-order.bin")
		assertParityText(t, row, boolText(len(goldenRecords) == 3 && goldenRecords[0].Payload == goldenRecords[1].Payload &&
			goldenRecords[0].Display != goldenRecords[1].Display && exactZshCPSelections(evidence)))
	case "multiplicity-preserved":
		assertParityText(t, row, boolText(evidence.Multiplicity && exactZshCPSelections(evidence) &&
			evidence.Selected[0] == evidence.Selected[2]))
	case "no-trailing-space":
		assertParityText(t, row, boolText(evidence.NoTrailingSpace))
	case "rejected":
		unknown := newParityRecord(protocol.KindFile, "unknown", []byte(filepath.Join(root, "unknown")))
		_, err := session.ValidateCP(snapshot, [][]byte{unknown.Wire().Bytes()}, []byte(root))
		assertParityText(t, row, boolText(errors.Is(err, session.ErrUnknownSelection)))
	default:
		t.Fatalf("unhandled Zsh semantic check %q", row.Check)
	}
}

func validateZshCDSelection(t *testing.T, snapshot session.Snapshot, record candidate.Record) bool {
	t.Helper()
	output := append([]byte("query\x00enter\x00"), record.Wire().Bytes()...)
	output = append(output, 0)
	parsed, err := fzf.ParseOutput(protocol.PickerCD, output, 0)
	if err != nil || parsed.Key != "enter" || len(parsed.Records) != 1 {
		return false
	}
	outcome, err := session.ValidateCD(snapshot, parsed.Records)
	return err == nil && len(outcome.Paths) == 1 && bytes.Equal(outcome.Paths[0], record.Path)
}

func validateZshCPSelection(t *testing.T, snapshot session.Snapshot, base string, first, second candidate.Record) bool {
	t.Helper()
	output := []byte("enter\x00")
	for _, record := range []candidate.Record{first, second, first} {
		output = append(output, record.Wire().Bytes()...)
		output = append(output, 0)
	}
	parsed, err := fzf.ParseOutput(protocol.PickerCP, output, 0)
	if err != nil || parsed.Key != "enter" || len(parsed.Records) != 3 {
		return false
	}
	for _, raw := range parsed.Records {
		if bytes.Count(raw, []byte{'\t'}) != 2 {
			return false
		}
	}
	outcome, err := session.ValidateCP(snapshot, parsed.Records, []byte(base))
	return err == nil && len(outcome.Paths) == 3 && bytes.Equal(outcome.Paths[0], pathutil.Relative([]byte(base), first.Path)) &&
		bytes.Equal(outcome.Paths[1], pathutil.Relative([]byte(base), second.Path)) && bytes.Equal(outcome.Paths[2], pathutil.Relative([]byte(base), first.Path))
}

func zshArgValue(arguments []string, option string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == option {
			return arguments[index+1]
		}
	}
	return ""
}

func exactZshCPSelections(evidence zshParityEvidence) bool {
	want := []string{evidence.Target, evidence.Second, evidence.Target}
	return reflect.DeepEqual(evidence.Selected, want)
}

func zshNavigationDestinationCount(t *testing.T, picker protocol.Picker, state session.State, record candidate.Record) int {
	t.Helper()
	count := 0
	for _, event := range []protocol.Event{{Opcode: protocol.OpForward, CurrentItem: record.Wire().Bytes()}, {Opcode: protocol.OpParent}, {Opcode: protocol.OpHome}} {
		actor, _ := newParityActor(t, state, []candidate.Record{record})
		if _, err := session.Handle(context.Background(), actor, event); err == nil {
			count++
		}
	}
	return count
}

func runZshBindingSemantic(t *testing.T, row parityRow) {
	_ = loadParityGolden[zshAdapterGolden](t, "zsh-adapter.json")
	picker := protocol.PickerCD
	if row.Case == "cp" {
		picker = protocol.PickerCP
	}
	root := t.TempDir()
	location := pathutil.Filesystem([]byte(root))
	state := parityState(picker, protocol.ModeAdd, location, location)
	actor, _ := newParityActor(t, state, nil)
	options := strings.Join(fzf.Options(picker, state.Prompt, pathutil.PromptDisplay(state.Location)), "\n")
	switch row.Check {
	case "slash-delegates":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpSlash, Query: []byte("nested")})
		assertParityText(t, row, boolText(err == nil && result.Effect.Put == "/"))
	case "inline-slash-branch-excluded":
		assertParityText(t, row, boolText(strings.Count(options, "transform(e:"+string(protocol.OpSlash)+")") == 1))
	case "add-tilde-put-precedes-navigation":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpHome, Query: []byte("nested")})
		assertParityText(t, row, boolText(err == nil && result.Effect.Put == "~" && result.Effect.ReloadGeneration == 0))
	case "initializes-mode":
		normal := parityState(picker, protocol.ModeNormal, location, location)
		modeActor, _ := newParityActor(t, normal, nil)
		result, err := session.Handle(context.Background(), modeActor, protocol.Event{Opcode: protocol.OpModeAdd})
		assertParityText(t, row, boolText(err == nil && result.Snapshot.State().Mode == protocol.ModeAdd && strings.HasPrefix(result.Snapshot.State().Prompt, "[A]")))
	case "binds-add":
		assertParityText(t, row, boolText(strings.Contains(options, "transform(e:"+string(protocol.OpModeAdd)+")")))
	case "binds-escape":
		assertParityText(t, row, boolText(strings.Contains(options, "transform(e:"+string(protocol.OpEscape)+")")))
	case "forward-transform-count":
		assertParityText(t, row, strconv.Itoa(strings.Count(options, "transform(e:"+string(protocol.OpForward)+")")))
	case "parent-transform-count":
		assertParityText(t, row, strconv.Itoa(strings.Count(options, "transform(e:"+string(protocol.OpParent)+")")))
	case "shift-tab-source-switching-excluded":
		assertParityText(t, row, boolText(!strings.Contains(options, "shift-tab")))
	default:
		t.Fatalf("unhandled Zsh binding check %q", row.Check)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func parityZshTarget(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zsh target with space")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
