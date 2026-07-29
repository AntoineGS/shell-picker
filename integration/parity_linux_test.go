//go:build linux

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
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
	target := filepath.Join(root, "target with space")
	second := filepath.Join(root, "second\npath")
	for _, path := range []string{target, second} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callLog := filepath.Join(root, "calls")
	statePath := filepath.Join(root, "state")
	fake := `emulate -LR zsh
for argument in "$@"; do
  print -rn -- "$argument"$'\0' >> "$SHELL_PICKER_PARITY_CALLS"
done
print -rn -- $'\0' >> "$SHELL_PICKER_PARITY_CALLS"
if [[ $1 == cd ]]; then
  print -rn -- "$SHELL_PICKER_PARITY_TARGET"$'\0'
else
  print -rn -- "$SHELL_PICKER_PARITY_TARGET"$'\0'"$SHELL_PICKER_PARITY_SECOND"$'\0'
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
cd -- "$SHELL_PICKER_PARITY_ROOT" || exit $?
if [[ $SHELL_PICKER_PARITY_PICKER == cd ]]; then
  BUFFER='before' CURSOR=3
  _shell_picker_cd
  buffer=$BUFFER
  trailing=1
else
  LBUFFER='cp ' RBUFFER=
  _shell_picker_cp
  buffer=$LBUFFER
  [[ $LBUFFER != *' ' ]] && trailing=1 || trailing=0
fi
emit() { print -rn -- "$1"$'\0' }
{
  emit start
  emit "$accepted"
  emit "$buffer"
  emit "$trailing"
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
		"HOME="+root, "TMPDIR="+root,
		"SHELL_PICKER_PARITY_PLUGIN="+filepath.Join(repository, "adapters", "zsh", "shell-picker.plugin.zsh"),
		"SHELL_PICKER_PARITY_ROOT="+root, "SHELL_PICKER_PARITY_PICKER="+string(picker),
		"SHELL_PICKER_PARITY_TARGET="+target, "SHELL_PICKER_PARITY_SECOND="+second,
		"SHELL_PICKER_PARITY_CALLS="+callLog, "SHELL_PICKER_PARITY_STATE="+statePath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Zsh parity widget: %v\n%s", err, output)
	}
	state := readNULRecords(t, statePath)
	calls := readNULRecords(t, callLog)
	if len(state) != 5 {
		t.Fatalf("Zsh parity state=%q", state)
	}
	accepted, err := strconv.Atoi(state[1])
	if err != nil {
		t.Fatal(err)
	}
	evidence := zshParityEvidence{
		Started: state[0] == "start", Ended: state[4] == "end", BufferNonblank: state[2] != "",
		NoTrailingSpace: state[3] == "1", OrderedPaths: strings.Contains(state[2], "target") && strings.Index(state[2], "target") < strings.Index(state[2], "second"),
		InvocationCount: 1, AcceptCount: accepted,
	}
	// The fake records every argv field, terminated by an empty frame.  A widget
	// invocation must use the public adapter's exact encoded-safe contract.
	if !equalZshArgs(calls, []string{string(picker), "--cwd", root, "--home", root, "--output", "nul"}) {
		t.Fatalf("Zsh picker argv=%q", calls)
	}
	if picker == protocol.PickerCD && state[2] != "builtin cd -- "+zshQuote(target) {
		t.Fatalf("Zsh cd buffer=%q", state[2])
	}
	return evidence
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

func zshQuote(value string) string { return strings.ReplaceAll(value, " ", "\\ ") }
