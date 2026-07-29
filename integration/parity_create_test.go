package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/preview"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

type createGolden struct {
	ValidQuery    string   `json:"valid_query"`
	ExistingQuery string   `json:"existing_query"`
	Invalid       []string `json:"invalid_queries"`
	ErrorPrefix   string   `json:"error_prefix"`
}

func runCreateSemantic(t *testing.T, row parityRow) {
	golden := loadParityGolden[createGolden](t, "create.json")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, golden.ExistingQuery), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing-file"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	query := []byte(golden.ValidQuery)
	switch row.Case {
	case "existing-directory":
		query = []byte(golden.ExistingQuery)
	case "invalid-empty":
		query = nil
	case "invalid-absolute":
		query = platformParityAbsoluteQuery(root)
	case "invalid-parent-escape":
		query = []byte("../escape")
	case "invalid-embedded-parent-escape":
		query = []byte("nested/../../escape")
	case "invalid-existing-file":
		query = []byte("existing-file")
	}
	if strings.HasPrefix(row.Case, "invalid-") {
		metadataCase := map[string]string{
			"invalid-empty": "", "invalid-absolute": "absolute", "invalid-parent-escape": "../escape",
			"invalid-embedded-parent-escape": "nested/../../escape", "invalid-existing-file": "existing-file",
		}[row.Case]
		if !containsString(golden.Invalid, metadataCase) {
			t.Fatalf("create golden has no invalid case %q", metadataCase)
		}
	}
	location := pathutil.Filesystem([]byte(root))
	state := parityState(protocol.PickerCD, protocol.ModeAdd, location, location)
	actor := session.New(context.Background(), func(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		records, err := candidate.EnumerateLocal(ctx, request.Picker, request.Location, candidate.LocalOptions{StatWorkers: 2})
		return candidate.BuildResult{Records: records}, err
	})
	t.Cleanup(func() { _ = actor.Close() })
	initial, err := actor.Apply(context.Background(), session.ProposedTransition{State: state, Build: &candidate.BuildRequest{Picker: state.Picker, Location: location}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	valid := row.Case == "new-directory" || row.Case == "existing-directory"
	target := filepath.Join(root, string(query))
	switch row.Check {
	case "exists":
		info, statErr := os.Stat(target)
		assertParityText(t, row, boolText(statErr == nil && info.IsDir()))
	case "directory-state":
		label := ""
		if bytes.Equal(result.Snapshot.State().Location.Path, []byte(target)) {
			label = string(query)
		}
		assertParityText(t, row, label)
	case "mode":
		assertParityText(t, row, string(result.Snapshot.State().Mode))
	case "source-mode":
		records := result.Snapshot.Records()
		local := len(records) >= 2 && records[0].Kind == protocol.KindLocal && bytes.Equal(records[0].Path, result.Snapshot.State().Location.Path)
		assertParityText(t, row, map[bool]string{true: "local"}[local])
	case "prompt":
		if valid {
			prefix := "[N] " + protocol.EscapeDisplay(query)
			matched := strings.HasPrefix(result.Snapshot.State().Prompt, "[N] ") && strings.Contains(result.Snapshot.State().Prompt, string(query))
			got := ""
			if matched {
				got = prefix + string(filepath.Separator) + " "
			}
			assertParityText(t, row, got)
		} else {
			matched := strings.HasPrefix(result.Snapshot.State().Prompt, golden.ErrorPrefix+" ")
			got := ""
			if matched {
				got = "[A!] current-directory/ "
			}
			assertParityText(t, row, got)
		}
	case "normal-keymap-activated":
		assertParityText(t, row, boolText(result.Effect.Rebind == protocol.ModeNormal))
	case "marks-cleared":
		assertParityText(t, row, boolText(result.Effect.ClearMulti))
	case "query-cleared":
		assertParityText(t, row, boolText(result.Effect.ClearQuery))
	case "candidates-reloaded":
		assertParityText(t, row, boolText(result.Effect.ReloadGeneration > initial.Snapshot.Generation()))
	case "directory-state-unchanged":
		assertParityText(t, row, boolText(bytes.Equal(result.Snapshot.State().Location.Path, initial.Snapshot.State().Location.Path)))
	case "source-mode-unchanged":
		assertParityText(t, row, boolText(result.Snapshot.State().Mode == initial.Snapshot.State().Mode))
	case "candidates-unchanged":
		assertParityText(t, row, boolText(reflectRecords(result.Snapshot.Records(), initial.Snapshot.Records())))
	case "prompt-refreshed":
		assertParityText(t, row, boolText(result.Effect.ErrorPrompt && result.Effect.Prompt != initial.Snapshot.State().Prompt))
	case "query-retained":
		assertParityText(t, row, boolText(!result.Effect.ClearQuery))
	case "candidates-not-reloaded":
		assertParityText(t, row, boolText(result.Effect.ReloadGeneration == 0))
	default:
		t.Fatalf("unhandled create check %q", row.Check)
	}
}

func runPreviewSemantic(t *testing.T, row parityRow) {
	_ = loadParityGolden[map[string]any](t, "preview.json")
	root := t.TempDir()
	path := platformParityPreviewPath(root, row.Case)
	if err := os.WriteFile(path, []byte("preview marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, literal := []byte(path), true
	if row.Case == "fzf-tab-second-field" {
		input, literal = []byte("display\u00a0"+path+" "), false
	}
	parsed, err := preview.ParseCompletionInput(input, literal, []byte(root))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	renderErr := preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: parsed.Path}, preview.Options{Stdout: &output, Limits: preview.DefaultLimits})
	assertParityText(t, row, boolText(renderErr == nil && bytes.Equal(parsed.Path, []byte(path)) && strings.Contains(output.String(), "preview marker")))
}
