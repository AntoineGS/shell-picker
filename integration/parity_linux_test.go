//go:build linux

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func platformParityExpected(row parityRow) string { return row.ExpectedText }

func platformParityDirectoryNames() []parityDirectoryName {
	return []parityDirectoryName{
		{"leading-dash-directory", "-leading-directory"},
		{"backslash-directory", `back\slash-directory`},
		{"control-byte-directory", "control\x01directory"},
		{"line-newline-directory", "line\ndirectory"},
		{"nbsp-directory", "nbsp\u00a0directory"},
		{"tab-directory", "tab\tdirectory"},
		{"trailing-space-directory", "trailing-directory "},
		{"space-directory", "space directory"},
	}
}

func platformParityRootLabel() string { return "root" }

func platformParityAbsoluteQuery(string) []byte { return []byte("/absolute") }

func platformParityPreviewPath(root, caseName string) string {
	switch caseName {
	case "literal-trailing-newline":
		return filepath.Join(root, "literal trailing newline\n")
	case "literal-nbsp":
		return filepath.Join(root, "literal\u00a0nbsp")
	default:
		return filepath.Join(root, "fzf-tab second field")
	}
}

func exerciseParityZshAdapter(t *testing.T, picker protocol.Picker) zshParityEvidence {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable; portable core semantics are covered on Windows")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "cwd space ' \" $(print cwd) `print tick` café\ncwd-line\n")
	home := filepath.Join(root, "home space ' \" $(print home) `print tick` Δ\nhome-line\n")
	target := filepath.Join(root, "target space ' \" $(print target) `print tick` 東京\ntarget-line\n")
	second := filepath.Join(root, "second space ' \" $(print second) `print tick` λ\nsecond-line\n")
	for _, path := range []string{cwd, home, target, second} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callLog := filepath.Join(root, "calls")
	selectionLog := filepath.Join(root, "selections")
	statePath := filepath.Join(root, "state")
	fake := `emulate -LR zsh
emit() { print -rn -- "$1"$'\0' }
for argument in "$@"; do
  emit "$argument" >> "$SHELL_PICKER_PARITY_CALLS"
done
emit '' >> "$SHELL_PICKER_PARITY_CALLS"
select_path() {
  emit "$1" >> "$SHELL_PICKER_PARITY_SELECTIONS"
  emit "$1"
}
if [[ $1 == cd ]]; then
  select_path "$SHELL_PICKER_PARITY_TARGET"
else
  select_path "$SHELL_PICKER_PARITY_TARGET"
  select_path "$SHELL_PICKER_PARITY_SECOND"
  select_path "$SHELL_PICKER_PARITY_TARGET"
fi
`
	writeExecutable(t, filepath.Join(bin, "shell-picker"), zsh, fake)
	runner := filepath.Join(root, "runner.zsh")
	runnerBody := `emulate -LR zsh
typeset -gi accepted=0 redisplayed=0 completed=0
zle() {
  case $1 in
    -N) ;;
    accept-line) (( ++accepted )) ;;
    redisplay) (( ++redisplayed )) ;;
    fzf_completion) (( ++completed )) ;;
    _shell_picker_cd | _shell_picker_cp) "$1" ;;
    *) return 91 ;;
  esac
}
bindkey() { return 0 }
source "$SHELL_PICKER_PARITY_PLUGIN" || exit $?
cd -- "$SHELL_PICKER_PARITY_CWD" || exit $?
if [[ $SHELL_PICKER_PARITY_PICKER == cd ]]; then
  BUFFER='before' CURSOR=3
  _shell_picker_cd
  buffer=$BUFFER
  expected="builtin cd -- ${(q)SHELL_PICKER_PARITY_TARGET}"
  trailing=1 ordered=1 multiplicity=1
else
  LBUFFER='cp ' RBUFFER=
  _shell_picker_cp
  buffer=$LBUFFER
  quoted=("${(q)SHELL_PICKER_PARITY_TARGET}" "${(q)SHELL_PICKER_PARITY_SECOND}" "${(q)SHELL_PICKER_PARITY_TARGET}")
  expected="cp -- ${(j: :)quoted}"
  [[ $LBUFFER != *' ' ]] && trailing=1 || trailing=0
  [[ $buffer == "$expected" ]] && ordered=1 || ordered=0
  [[ $buffer == "$expected" ]] && multiplicity=1 || multiplicity=0
fi
[[ $buffer == "$expected" ]] && equal=1 || equal=0
emit() { print -rn -- "$1"$'\0' }
{
  emit start
  emit "$accepted"
  emit "$buffer"
  emit "$expected"
  emit "$equal"
  emit "$ordered"
  emit "$multiplicity"
  emit "$trailing"
  emit "$PWD"
  emit "$HOME"
  emit "$SHELL_PICKER_PARITY_TARGET"
  emit "$SHELL_PICKER_PARITY_SECOND"
  emit end
} >| "$SHELL_PICKER_PARITY_STATE"
`
	if err := os.WriteFile(runner, []byte(runnerBody), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(zsh, "-f", runner)
	command.Env = replaceEnvironment(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home, "TMPDIR="+root,
		"SHELL_PICKER_PARITY_PLUGIN="+filepath.Join(repository, "adapters", "zsh", "shell-picker.plugin.zsh"),
		"SHELL_PICKER_PARITY_CWD="+cwd, "SHELL_PICKER_PARITY_PICKER="+string(picker),
		"SHELL_PICKER_PARITY_TARGET="+target, "SHELL_PICKER_PARITY_SECOND="+second,
		"SHELL_PICKER_PARITY_CALLS="+callLog, "SHELL_PICKER_PARITY_SELECTIONS="+selectionLog,
		"SHELL_PICKER_PARITY_STATE="+statePath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Zsh parity widget: %v\n%s", err, output)
	}
	state := readNULRecords(t, statePath)
	calls := readNULRecords(t, callLog)
	selected := readNULRecords(t, selectionLog)
	if len(state) != 13 {
		t.Fatalf("Zsh parity state=%q", state)
	}
	accepted, err := strconv.Atoi(state[1])
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{string(picker), "--cwd", cwd, "--home", home, "--output", "nul"}
	if !equalZshArgs(calls, wantArgs) {
		t.Fatalf("Zsh picker argv=%q, want %q", calls, wantArgs)
	}
	wantSelected := []string{target}
	if picker == protocol.PickerCP {
		wantSelected = []string{target, second, target}
	}
	if !reflect.DeepEqual(selected, wantSelected) {
		t.Fatalf("Zsh selected paths=%q, want %q", selected, wantSelected)
	}
	evidence := zshParityEvidence{
		Started: state[0] == "start", Ended: state[12] == "end", BufferEqual: state[4] == "1",
		OrderedPaths: state[5] == "1", Multiplicity: state[6] == "1", NoTrailingSpace: state[7] == "1",
		InvocationCount: 1, AcceptCount: accepted, Buffer: state[2], ExpectedBuffer: state[3], CWD: state[8], Home: state[9],
		Target: state[10], Second: state[11], Args: append([]string(nil), calls[:len(calls)-1]...), Selected: selected,
	}
	if evidence.CWD != cwd || evidence.Home != home || evidence.Target != target || evidence.Second != second ||
		!evidence.BufferEqual || evidence.Buffer != evidence.ExpectedBuffer || picker == protocol.PickerCD && evidence.AcceptCount != 1 {
		t.Fatalf("Zsh hostile evidence=%+v", evidence)
	}
	return evidence
}

func TestParityZshAdapterHostilePaths(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		t.Run(string(picker), func(t *testing.T) {
			evidence := exerciseParityZshAdapter(t, picker)
			if !evidence.BufferEqual || evidence.Buffer != evidence.ExpectedBuffer || !strings.Contains(evidence.CWD, "\n") ||
				!strings.Contains(evidence.Home, "\n") || !strings.Contains(evidence.Target, "$(") || !strings.Contains(evidence.Target, "`") {
				t.Fatalf("hostile Zsh evidence=%+v", evidence)
			}
			if picker == protocol.PickerCP && (!evidence.OrderedPaths || !evidence.Multiplicity || !evidence.NoTrailingSpace ||
				!reflect.DeepEqual(evidence.Selected, []string{evidence.Target, evidence.Second, evidence.Target})) {
				t.Fatalf("hostile CP evidence=%+v", evidence)
			}
		})
	}
}

func equalZshArgs(frames, want []string) bool {
	if len(frames) != len(want)+1 || frames[len(frames)-1] != "" {
		return false
	}
	for i := range want {
		if frames[i] != want[i] {
			return false
		}
	}
	return true
}
