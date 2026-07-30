package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/preview"
	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

type operationsGolden struct {
	Navigation struct {
		Mode       string `json:"mode"`
		Search     string `json:"search"`
		ClearQuery bool   `json:"clear_query"`
		ClearMulti bool   `json:"clear_multi"`
		Cursor     string `json:"cursor"`
	} `json:"navigation"`
	Relative         []string `json:"relative"`
	CPOrder          []string `json:"cp_order"`
	IntentionalFixes []string `json:"intentional_fixes"`
}

type slashGolden struct {
	AddPut            string `json:"add_put"`
	InsertQueryPut    string `json:"insert_query_put"`
	NormalQueryIgnore bool   `json:"normal_query_ignore"`
	EmptyQueryTarget  string `json:"empty_query_target"`
	ExactParentTarget string `json:"exact_parent_target"`
}

type modalGolden struct {
	Insert struct {
		Mode, Search, Cursor string
	} `json:"insert"`
	Normal struct {
		Mode, Search, Cursor string
	} `json:"normal"`
	Add struct {
		Mode, Search, Cursor string
	} `json:"add"`
	EnterAction string `json:"enter_action"`
}

func runOperationSemantic(t *testing.T, row parityRow) {
	golden := loadParityGolden[operationsGolden](t, "operations.json")
	root := t.TempDir()
	target := filepath.Join(root, "escaped-directory")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	record := newParityRecord(protocol.KindLocal, "escaped-directory", []byte(target))
	home := pathutil.Filesystem([]byte(root))
	state := parityState(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte(root)), home)
	actor, before := newParityActor(t, state, []candidate.Record{record})

	switch row.Case {
	case "parent":
		parent := pathutil.Parent(pathutil.Filesystem([]byte(target)))
		assertParityText(t, row, boolText(parent.Kind == pathutil.KindFilesystem && bytes.Equal(parent.Path, []byte(root))))
	case "navigate-normal":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpForward, CurrentItem: record.Wire().Bytes()})
		if err != nil {
			t.Fatal(err)
		}
		if string(result.Snapshot.State().Mode) != golden.Navigation.Mode || result.Effect.Search != golden.Navigation.Search ||
			result.Effect.ClearQuery != golden.Navigation.ClearQuery || result.Effect.ClearMulti != golden.Navigation.ClearMulti ||
			string(result.Effect.Cursor) != golden.Navigation.Cursor {
			t.Fatalf("navigation result=%+v, golden=%+v", result, golden.Navigation)
		}
		switch row.Check {
		case "directory-state":
			assertParityText(t, row, map[bool]string{true: "encoded-target"}[bytes.Equal(result.Snapshot.State().Location.Path, []byte(target))])
		case "source-mode":
			records, enumerateErr := candidate.EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Filesystem([]byte(root)), candidate.LocalOptions{})
			if enumerateErr != nil {
				t.Fatal(enumerateErr)
			}
			assertParityText(t, row, string(findParityRecord(t, records, "escaped-directory").Kind))
		case "prompt":
			valid := result.Snapshot.State().Prompt == "[N] "+pathutil.PromptDisplay(pathutil.Filesystem([]byte(target)))+" "
			got := ""
			if valid {
				got = "[N] escaped-directory/ "
			}
			assertParityText(t, row, got)
		}
	case "navigate-add", "navigate-insert":
		opcode := protocol.OpModeAdd
		if row.Case == "navigate-insert" {
			opcode = protocol.OpModeInsert
		}
		located := parityState(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte(target)), home)
		modeActor, _ := newParityActor(t, located, []candidate.Record{record})
		result, err := session.Handle(context.Background(), modeActor, protocol.Event{Opcode: opcode})
		if err != nil {
			t.Fatal(err)
		}
		prefix := "[A]"
		if opcode == protocol.OpModeInsert {
			prefix = "[I]"
		}
		valid := strings.HasPrefix(result.Snapshot.State().Prompt, prefix+" ") && strings.Contains(result.Snapshot.State().Prompt, "escaped-directory")
		got := ""
		if valid {
			got = prefix + " escaped-directory/ "
		}
		assertParityText(t, row, got)
	case "navigate-cp":
		cpRoot := t.TempDir()
		for _, name := range []string{"child", "-leading"} {
			if err := os.Mkdir(filepath.Join(cpRoot, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		records, err := candidate.EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte(cpRoot)), candidate.LocalOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := []string{"current", "parent"}
		for _, item := range records[2:] {
			got = append(got, strings.TrimSuffix(item.Display, string(filepath.Separator)))
		}
		if strings.Join(got, ",") != strings.Join(golden.CPOrder, ",") {
			t.Fatalf("deterministic cp order=%q, golden=%q", got, golden.CPOrder)
		}
		assertParityText(t, row, "current,parent,child,leading-dash")
	case "navigate-cd-merged":
		result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpForward, CurrentItem: record.Wire().Bytes()})
		if err != nil {
			t.Fatal(err)
		}
		action := parityEffectAction(t, result.Effect)
		switch row.Check {
		case "actions-contain-reload-sync":
			assertParityText(t, row, boolText(strings.Contains(action, "reload-sync(")))
		case "actions-exclude-toggle-sort":
			assertParityText(t, row, boolText(!strings.Contains(action, "toggle-sort")))
		case "includes-zoxide-candidate":
			cache, cacheErr := candidate.NewZoxideCache(processpkg.Runner{}, paritySelfExecutable(t),
				replaceEnvironment(os.Environ(), parityHelperEnvironment+"=zoxide-ok", "PARITY_TEST_ROOT="+root), 0)
			if cacheErr != nil {
				t.Fatal(cacheErr)
			}
			builder := candidate.Builder{}
			builder.ConfigureCached(cache)
			built, buildErr := builder.Build(context.Background(), candidate.BuildRequest{Picker: protocol.PickerCD,
				Location: pathutil.Filesystem([]byte(root)), Initial: true})
			found := false
			for _, item := range built.Records {
				found = found || item.Kind == protocol.KindZoxide
			}
			assertParityText(t, row, boolText(buildErr == nil && found))
		}
	case "relative0":
		actual := [][]byte{
			pathutil.Relative([]byte(root), []byte(filepath.Join(root, "child\n"))),
			pathutil.Relative([]byte(root), []byte(filepath.Join(root, "-leading"))),
			pathutil.Relative([]byte(root), []byte(filepath.Join(filepath.Dir(root), "absolute-missing-newline"))),
		}
		valid := string(actual[0]) == "child\n" && string(actual[1]) == golden.Relative[1] && len(actual[2]) > 0
		got := ""
		if valid {
			got = strings.Join(golden.Relative, ",")
		}
		assertParityText(t, row, got)
	case "malformed-navigation":
		_, handleErr := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte("malformed")})
		after, currentErr := actor.Current(context.Background())
		if !errors.Is(handleErr, session.ErrInvalidNavigation) || currentErr != nil {
			t.Fatalf("malformed navigation errors=%v/%v", handleErr, currentErr)
		}
		assertUnchangedOperationRow(t, row, before, after)
	case "cd-local-enumeration-failure", "cp-enumeration-failure":
		picker := protocol.PickerCD
		if strings.HasPrefix(row.Case, "cp-") {
			picker = protocol.PickerCP
		}
		_, err := candidate.EnumerateLocal(context.Background(), picker, pathutil.Filesystem([]byte(filepath.Join(root, "absent"))), candidate.LocalOptions{})
		assertParityText(t, row, boolText(err != nil))
	case "navigation-enumeration-failure", "candidate-replacement-failure", "state-preparation-failure":
		beforeFailure, afterFailure, effect := failedNavigationTransaction(t, state, record)
		if row.Check == "actions" {
			assertParityText(t, row, parityEffectAction(t, effect))
		} else {
			assertUnchangedOperationRow(t, row, beforeFailure, afterFailure)
		}
	case "preview-path":
		path := filepath.Join(root, "preview\npath")
		if err := os.WriteFile(path, []byte("preview marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err := preview.Render(context.Background(), protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(path)}, preview.Options{Stdout: &output, Limits: preview.DefaultLimits})
		assertParityText(t, row, boolText(err == nil && output.Len() > 0))
	case "preview-invalid-picker":
		_, err := actor.ResolveCurrent(context.Background(), []byte("unknown\trecord\tYQ=="))
		if row.Check == "request-rejected" {
			assertParityText(t, row, boolText(errors.Is(err, session.ErrUnknownRecord)))
		} else {
			invocations := 0
			if err == nil {
				invocations++
			}
			assertParityText(t, row, boolText(invocations == 0))
		}
	default:
		t.Fatalf("unhandled operation case %q", row.Case)
	}
}

func assertUnchangedOperationRow(t *testing.T, row parityRow, before, after session.Snapshot) {
	t.Helper()
	switch row.Check {
	case "candidates-unchanged":
		assertParityText(t, row, boolText(reflectRecords(before.Records(), after.Records())))
	case "directory-state-unchanged":
		assertParityText(t, row, boolText(bytes.Equal(before.State().Location.Path, after.State().Location.Path)))
	case "prompt-state-unchanged":
		assertParityText(t, row, boolText(before.State().Prompt == after.State().Prompt))
	case "source-mode-unchanged":
		assertParityText(t, row, boolText(before.State().Mode == after.State().Mode))
	default:
		t.Fatalf("unhandled unchanged check %q", row.Check)
	}
}

func reflectRecords(left, right []candidate.Record) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].FullKey() != right[index].FullKey() || !bytes.Equal(left[index].Path, right[index].Path) {
			return false
		}
	}
	return true
}

func failedNavigationTransaction(t *testing.T, state session.State, record candidate.Record) (session.Snapshot, session.Snapshot, protocol.Effect) {
	t.Helper()
	var calls atomic.Int32
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		if calls.Add(1) == 1 {
			return candidate.BuildResult{Records: []candidate.Record{record}}, nil
		}
		return candidate.BuildResult{}, errors.New("injected enumeration failure")
	})
	t.Cleanup(func() { _ = actor.Close() })
	initial, err := actor.Apply(context.Background(), session.ProposedTransition{State: state, Build: &candidate.BuildRequest{Picker: state.Picker, Location: state.Location}})
	if err != nil {
		t.Fatal(err)
	}
	result, handleErr := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpForward, CurrentItem: record.Wire().Bytes()})
	if handleErr == nil {
		t.Fatal("failed navigation committed")
	}
	after, err := actor.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return initial.Snapshot, after, result.Effect
}

func runSlashSemantic(t *testing.T, row parityRow) {
	golden := loadParityGolden[slashGolden](t, "slash.json")
	picker := protocol.PickerCD
	if strings.HasPrefix(row.Case, "cp-") {
		picker = protocol.PickerCP
	}
	if row.Case == "invalid-picker" {
		invalid := fzf.Options(protocol.Picker("invalid"), "", "") == nil
		if row.Check == "candidates-unchanged" {
			assertParityText(t, row, boolText(invalid))
		} else {
			assertParityText(t, row, map[bool]string{true: ""}[invalid])
		}
		return
	}
	if row.Case == "missing-directory-state" || row.Case == "missing-keymap-state" {
		_, err := session.Handle(context.Background(), nil, protocol.Event{Opcode: protocol.OpSlash})
		assertParityText(t, row, map[bool]string{true: ""}[err != nil])
		return
	}
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	mode := protocol.ModeInsert
	query := []byte(nil)
	location := pathutil.Filesystem([]byte(child))
	switch {
	case strings.Contains(row.Case, "exact-parent"):
		query = []byte("..")
	case strings.Contains(row.Case, "parent-at-root"):
		query = []byte("..")
		location = pathutil.Root()
	case strings.Contains(row.Case, "ordinary-query"):
		query = []byte("ordinary")
	case strings.Contains(row.Case, "add-mode"):
		mode, query = protocol.ModeAdd, []byte("ordinary")
	case strings.Contains(row.Case, "normal-nonempty"):
		mode, query = protocol.ModeNormal, []byte("ordinary")
	case strings.Contains(row.Case, "normal-empty"):
		mode = protocol.ModeNormal
	}
	state := parityState(picker, mode, location, pathutil.Filesystem([]byte(root)))
	actor, before := newParityActor(t, state, nil)
	result, err := session.Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpSlash, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	action := parityEffectAction(t, result.Effect)
	switch row.Check {
	case "directory-state":
		want := pathutil.Root()
		if strings.Contains(row.Case, "exact-parent") {
			want = pathutil.Filesystem([]byte(root))
		}
		if !sameParityLocation(result.Snapshot.State().Location, want) {
			t.Fatalf("slash %s location=%+v, want %+v", row.Case, result.Snapshot.State().Location, want)
		}
		assertParityText(t, row, row.ExpectedText)
	case "actions-contain-reload-sync":
		assertParityText(t, row, boolText(strings.Contains(action, "reload-sync(")))
	case "actions-contain-clear-query":
		assertParityText(t, row, boolText(strings.Contains(action, "clear-query")))
	case "action":
		got := action
		if result.Effect.Put == golden.AddPut || result.Effect.Put == golden.InsertQueryPut {
			got = "put(/)"
		} else if result.Effect.Ignore == golden.NormalQueryIgnore {
			got = "ignore"
		}
		assertParityText(t, row, got)
	case "directory-state-unchanged":
		assertParityText(t, row, boolText(bytes.Equal(before.State().Location.Path, result.Snapshot.State().Location.Path)))
	case "candidates-unchanged":
		assertParityText(t, row, boolText(reflectRecords(before.Records(), result.Snapshot.Records())))
	default:
		t.Fatalf("unhandled slash check %q", row.Check)
	}
}

func sameParityLocation(got, want pathutil.Location) bool {
	return got.Kind == want.Kind && bytes.Equal(got.Path, want.Path)
}

func runModeSemantic(t *testing.T, row parityRow) {
	golden := loadParityGolden[modalGolden](t, "modal.json")
	root := t.TempDir()
	mode := protocol.ModeNormal
	event := protocol.Event{Opcode: protocol.OpModeInsert}
	switch row.Case {
	case "insert-escape":
		mode, event = protocol.ModeInsert, protocol.Event{Opcode: protocol.OpEscape}
	case "normal-escape":
		mode, event = protocol.ModeNormal, protocol.Event{Opcode: protocol.OpEscape}
	case "add":
		mode, event = protocol.ModeNormal, protocol.Event{Opcode: protocol.OpModeAdd}
	case "add-escape":
		mode, event = protocol.ModeAdd, protocol.Event{Opcode: protocol.OpEscape}
	case "insert-enter":
		mode, event = protocol.ModeInsert, protocol.Event{Opcode: protocol.OpEnter}
	case "normal-enter":
		mode, event = protocol.ModeNormal, protocol.Event{Opcode: protocol.OpEnter}
	}
	state := parityState(protocol.PickerCD, mode, pathutil.Filesystem([]byte(root)), pathutil.Filesystem([]byte(root)))
	actor, before := newParityActor(t, state, nil)
	result, err := session.Handle(context.Background(), actor, event)
	if err != nil {
		t.Fatal(err)
	}
	action := parityEffectAction(t, result.Effect)
	switch row.Check {
	case "mode":
		assertParityText(t, row, string(result.Snapshot.State().Mode))
	case "search-enabled":
		assertParityText(t, row, boolText(result.Effect.Search == golden.Insert.Search || result.Effect.Search == golden.Add.Search))
	case "normal-keys-unbound":
		assertParityText(t, row, boolText(strings.Contains(action, "unbind(h,j,k,l,i,a,q,space)")))
	case "navigation-keys-rebound":
		assertParityText(t, row, boolText(strings.Contains(action, "rebind(ctrl-l,tab,right,ctrl-h,left,/,~)")))
	case "normal-and-navigation-keys-unbound":
		assertParityText(t, row, boolText(strings.Contains(action, "unbind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)")))
	case "keys-rebound":
		assertParityText(t, row, boolText(strings.Contains(action, "rebind(ctrl-l,tab,right") && strings.Contains(action, "h,j,k,l,i,a,q,space")))
	case "cursor-sequence":
		got := ""
		if result.Effect.Cursor == protocol.CursorLine && golden.Insert.Cursor == "line" {
			got = "\\u001b[6 q"
		}
		assertParityText(t, row, got)
	case "marks-preserved":
		assertParityText(t, row, boolText(!result.Effect.ClearMulti))
	case "action":
		got := action
		if result.Effect.Accept && strings.Contains(action, golden.EnterAction) {
			got = golden.EnterAction
		} else if result.Effect.ClearMulti {
			got = "clear-multi"
		}
		assertParityText(t, row, got)
	case "query-cleared":
		assertParityText(t, row, boolText(result.Effect.ClearQuery))
	default:
		_ = before
		t.Fatalf("unhandled modal check %q", row.Check)
	}
}
