package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

const parityHelperEnvironment = "PARITY_TEST_HELPER"

var semanticParityRunners = map[string]parityRunner{
	"codec-roundtrip":        runCodecRoundtrip,
	"record-shape":           runCodecRecord,
	"candidate-kind":         runCodecRecord,
	"candidate-display":      runCodecRecord,
	"candidate-payload":      runCodecRecord,
	"codec-reject":           runCodecRoundtrip,
	"batch-golden":           runBatchEncoder,
	"batch-error":            runBatchEncoder,
	"enumeration":            runEnumeration,
	"merge":                  runMerge,
	"operation":              runOperation,
	"slash":                  runSlash,
	"mode":                   runMode,
	"create":                 runCreate,
	"preview":                runPreviewRow,
	"zsh-cd":                 runZshCD,
	"zsh-cp":                 runZshCP,
	"zsh-query-binding":      runZshQueryBinding,
	"zsh-navigation-binding": runZshNavigationBinding,
}

func init() {
	parityRunners = map[string]parityRunner{
		"codec-roundtrip":                    dispatchParitySemantic,
		"codec-records":                      dispatchParitySemantic,
		"batch-encoder":                      dispatchParitySemantic,
		"directory-enumeration":              dispatchParitySemantic,
		"cd-merged":                          dispatchParitySemantic,
		"operations":                         dispatchParitySemantic,
		"slash":                              dispatchParitySemantic,
		"modal":                              dispatchParitySemantic,
		"create":                             dispatchParitySemantic,
		"preview":                            dispatchParitySemantic,
		"zshrc-cd":                           dispatchParitySemantic,
		"zshrc-cp":                           dispatchParitySemantic,
		"zshrc-add-mode-query-bindings":      dispatchParitySemantic,
		"zshrc-add-mode-navigation-bindings": dispatchParitySemantic,
	}
}

func dispatchParitySemantic(t *testing.T, row parityRow) {
	t.Helper()
	name := semanticRunnerName(row)
	runner, ok := semanticParityRunners[name]
	if !ok {
		t.Fatalf("row %s has no semantic runner for %s/%s", row.ID, row.Runner, row.Check)
	}
	runner(t, row)
}

func semanticRunnerName(row parityRow) string {
	switch row.Runner {
	case "codec-roundtrip":
		if row.Check == "decode-rejected" {
			return "codec-reject"
		}
		return "codec-roundtrip"
	case "codec-records":
		switch row.Check {
		case "record-tab-count", "record-count":
			return "record-shape"
		case "kind":
			return "candidate-kind"
		case "escaped-display", "display-excludes-octal-escape":
			return "candidate-display"
		default:
			return "candidate-payload"
		}
	case "batch-encoder":
		if row.Check == "exit-failure-propagated" {
			return "batch-error"
		}
		return "batch-golden"
	case "directory-enumeration":
		return "enumeration"
	case "cd-merged":
		return "merge"
	case "operations":
		return "operation"
	case "modal":
		return "mode"
	case "zshrc-cd":
		return "zsh-cd"
	case "zshrc-cp":
		return "zsh-cp"
	case "zshrc-add-mode-query-bindings":
		return "zsh-query-binding"
	case "zshrc-add-mode-navigation-bindings":
		return "zsh-navigation-binding"
	default:
		return row.Runner
	}
}

func runParityRow(t *testing.T, row parityRow) {
	t.Helper()
	runner, ok := parityRunners[row.Runner]
	if !ok {
		t.Fatalf("row %s has unknown runner %q", row.ID, row.Runner)
	}
	runner(t, row)
}

func assertParityText(t *testing.T, row parityRow, got string) {
	t.Helper()
	want := platformParityExpected(row)
	if got != want {
		t.Fatalf("%s %s/%s=%q, want %q", row.ID, row.Case, row.Check, got, want)
	}
}

func boolText(value bool) string { return strconv.FormatBool(value) }

func runOperation(t *testing.T, row parityRow)            { runOperationSemantic(t, row) }
func runSlash(t *testing.T, row parityRow)                { runSlashSemantic(t, row) }
func runMode(t *testing.T, row parityRow)                 { runModeSemantic(t, row) }
func runCreate(t *testing.T, row parityRow)               { runCreateSemantic(t, row) }
func runPreviewRow(t *testing.T, row parityRow)           { runPreviewSemantic(t, row) }
func runZshCD(t *testing.T, row parityRow)                { runZshSemantic(t, row, protocol.PickerCD) }
func runZshCP(t *testing.T, row parityRow)                { runZshSemantic(t, row, protocol.PickerCP) }
func runZshQueryBinding(t *testing.T, row parityRow)      { runZshBindingSemantic(t, row) }
func runZshNavigationBinding(t *testing.T, row parityRow) { runZshBindingSemantic(t, row) }

func loadParityGolden[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "parity", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return value
}

func newParityRecord(kind protocol.Kind, display string, path []byte) candidate.Record {
	return candidate.Record{Kind: kind, Display: display, Path: bytes.Clone(path), Payload: protocol.EncodePath(path), Target: pathutil.Filesystem(path)}
}

func newParityActor(t *testing.T, state session.State, records []candidate.Record) (*session.Actor, session.Snapshot) {
	t.Helper()
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: records}, nil
	})
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("close parity actor: %v", err)
		}
	})
	result, err := actor.Apply(context.Background(), session.ProposedTransition{State: state, Build: &candidate.BuildRequest{Picker: state.Picker, Location: state.Location}})
	if err != nil {
		t.Fatal(err)
	}
	return actor, result.Snapshot
}

func parityState(picker protocol.Picker, mode protocol.Mode, location, home pathutil.Location) session.State {
	prefix := "[I]"
	if mode == protocol.ModeNormal {
		prefix = "[N]"
	} else if mode == protocol.ModeAdd {
		prefix = "[A]"
	}
	return session.State{Picker: picker, Mode: mode, Location: location, Home: home,
		Prompt: prefix + " " + pathutil.PromptDisplay(location) + " "}
}

func parityEffectAction(t *testing.T, effect protocol.Effect) string {
	t.Helper()
	action, err := fzf.RenderEffect(effect)
	if err != nil {
		t.Fatal(err)
	}
	return action
}
