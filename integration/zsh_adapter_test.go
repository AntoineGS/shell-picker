package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type zshAdapterFixture struct {
	zsh         string
	plugin      string
	runner      string
	cwd         string
	home        string
	output      string
	state       string
	calls       string
	cpCalls     string
	unexpected  string
	environment []string
}

func TestZshAdapter(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable")
	}
	zsh, err = filepath.Abs(zsh)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(repository, "adapters", "zsh", "shell-picker.plugin.zsh")
	suite := filepath.Join(repository, "adapters", "zsh", "shell-picker.plugin.test.zsh")

	t.Run("syntax", func(t *testing.T) {
		command := exec.Command(zsh, "-n", plugin)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("zsh syntax: %v\n%s", err, output)
		}
	})

	t.Run("direct-suite", func(t *testing.T) {
		command := exec.Command(zsh, suite)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("zsh adapter suite: %v\n%s", err, output)
		} else if string(output) != "zsh adapter tests: PASS\n" {
			t.Fatalf("zsh adapter suite output=%q", output)
		}
	})

	edges := []struct {
		name string
		path string
	}{
		{"tab", "tab\tpath"},
		{"newline", "line\npath"},
		{"nbsp", "nbsp\u00a0path"},
		{"leading-dash", "-leading"},
		{"backslash", `back\slash`},
		{"apostrophe", "apost'rophe"},
		{"trailing-space", "trailing "},
	}
	for _, edge := range edges {
		t.Run("cd-"+edge.name, func(t *testing.T) {
			fixture := newZshAdapterFixture(t, zsh, plugin)
			state := fixture.run(t, "cd", []string{edge.path})
			assertZshAdapterState(t, state, 1, 0, []string{edge.path})
			fixture.assertOnePicker(t, "cd")
		})
	}

	t.Run("space-trigger", func(t *testing.T) {
		fixture := newZshAdapterFixture(t, zsh, plugin)
		state := fixture.run(t, "space", []string{"space target"})
		assertZshAdapterState(t, state, 1, 0, []string{"space target"})
		fixture.assertOnePicker(t, "cd")
	})

	t.Run("cp-tab-order-duplicates", func(t *testing.T) {
		fixture := newZshAdapterFixture(t, zsh, plugin)
		paths := []string{"first path", "tab\tpath", "line\npath", "-leading", "trailing ", "first path"}
		state := fixture.run(t, "tab", paths)
		assertZshAdapterState(t, state, 0, 0, append([]string{"--"}, paths...))
		if strings.HasSuffix(state[3], " ") {
			t.Fatalf("cp LBUFFER has trailing space: %q", state[3])
		}
		fixture.assertOnePicker(t, "cp")
		fixture.assertCPArgs(t, append([]string{"--"}, paths...))
	})

	terminators := []struct {
		name     string
		scenario string
	}{
		{"insert", "tab-options"},
		{"existing", "tab-terminator"},
		{"escaped-existing", "tab-escaped-terminator"},
	}
	for _, terminator := range terminators {
		t.Run("cp-option-terminator-"+terminator.name, func(t *testing.T) {
			fixture := newZshAdapterFixture(t, zsh, plugin)
			state := fixture.run(t, terminator.scenario, []string{"-leading", "duplicate", "duplicate"})
			wantArgs := []string{"-a", "existing", "--", "-leading", "duplicate", "duplicate"}
			if terminator.scenario != "tab-options" {
				wantArgs = []string{"-a", "--", "existing", "-leading", "duplicate", "duplicate"}
			}
			if strings.HasSuffix(state[3], " ") {
				t.Fatalf("cp LBUFFER has trailing space: %q", state[3])
			}
			fixture.assertOnePicker(t, "cp")
			fixture.assertCPArgs(t, wantArgs)
		})
	}

	contexts := []struct {
		name    string
		lbuffer string
		want    []string
	}{
		{"redirect", "cp existing > -- ", []string{"existing", "--", "-leading", "duplicate", "duplicate"}},
		{"fd-redirect", "cp existing 2>> -- ", []string{"existing", "--", "-leading", "duplicate", "duplicate"}},
		{"short-target", "cp -t -- existing ", []string{"-t", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"short-suffix", "cp -S -- existing ", []string{"-S", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"short-cluster", "cp -avt -- existing ", []string{"-avt", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"short-target-attached", "cp -t-- existing ", []string{"-t--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"short-suffix-attached", "cp -S-- existing ", []string{"-S--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"short-attached-then-effective", "cp -tfoo -- existing ", []string{"-tfoo", "--", "existing", "-leading", "duplicate", "duplicate"}},
		{"long-target", "cp --target-directory -- existing ", []string{"--target-directory", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"long-suffix", "cp --suffix -- existing ", []string{"--suffix", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"long-target-attached", "cp --target-directory=-- existing ", []string{"--target-directory=--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"long-suffix-attached", "cp --suffix=-- existing ", []string{"--suffix=--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"plain-effective", "cp -- existing ", []string{"--", "existing", "-leading", "duplicate", "duplicate"}},
		{"escaped-effective", `cp \-- existing `, []string{"--", "existing", "-leading", "duplicate", "duplicate"}},
		{"separator-reset", "cp -- prior ; cp -t -- existing ", []string{"-t", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"here-string", "cp existing <<< -- ", []string{"existing", "--", "-leading", "duplicate", "duplicate"}},
		{"combined-output", "cp existing &>> -- ", []string{"existing", "--", "-leading", "duplicate", "duplicate"}},
		{"multiple-redirections", "cp -t > first 2>> second <<< input -- existing ", []string{"-t", "--", "existing", "--", "-leading", "duplicate", "duplicate"}},
		{"quoted-here-string", `cp existing "<<<" -- `, []string{"existing", "<<<", "--", "-leading", "duplicate", "duplicate"}},
		{"escaped-here-string", `cp existing \<\<\< -- `, []string{"existing", "<<<", "--", "-leading", "duplicate", "duplicate"}},
		{"quoted-combined-output", `cp existing "&>" -- `, []string{"existing", "&>", "--", "-leading", "duplicate", "duplicate"}},
		{"escaped-combined-output", `cp existing \&\> -- `, []string{"existing", "&>", "--", "-leading", "duplicate", "duplicate"}},
	}
	for _, context := range contexts {
		t.Run("cp-terminator-context-"+context.name, func(t *testing.T) {
			fixture := newZshAdapterFixture(t, zsh, plugin)
			state := fixture.runCP(t, context.lbuffer, []string{"-leading", "duplicate", "duplicate"})
			if strings.HasSuffix(state[3], " ") {
				t.Fatalf("cp LBUFFER has trailing space: %q", state[3])
			}
			fixture.assertOnePicker(t, "cp")
			fixture.assertCPArgs(t, context.want)
		})
	}

	for _, operator := range []string{"<<", "<<-"} {
		t.Run("cp-terminator-context-heredoc-"+operator, func(t *testing.T) {
			fixture := newZshAdapterFixture(t, zsh, plugin)
			state := fixture.runCPHeredoc(t, "cp existing "+operator+" -- ", []string{"-leading", "duplicate", "duplicate"})
			if strings.HasSuffix(state[3], " ") {
				t.Fatalf("cp LBUFFER has trailing space: %q", state[3])
			}
			fixture.assertOnePicker(t, "cp")
			fixture.assertCPArgs(t, []string{"existing", "--", "-leading", "duplicate", "duplicate"})
		})
	}

	t.Run("invalid-utf8-bytes", func(t *testing.T) {
		locales := []struct {
			name  string
			value string
		}{{"c", "C"}}
		if locale, ok := findZshUTF8Locale(zsh); ok {
			locales = append(locales, struct {
				name  string
				value string
			}{"utf8", locale})
		} else {
			t.Run("utf8", func(t *testing.T) { t.Skip("no locale reports a UTF-8 codeset to zsh") })
		}
		for _, locale := range locales {
			t.Run(locale.name, func(t *testing.T) {
				fixture := newZshAdapterFixture(t, zsh, plugin)
				invalid := string([]byte{'i', 'n', 'v', 'a', 'l', 'i', 'd', '-', 0xff, '-', 0xfe})
				if err := os.WriteFile(filepath.Join(fixture.cwd, invalid), []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				state := fixture.runLocale(t, "tab", []string{invalid}, locale.value)
				assertZshAdapterState(t, state, 0, 0, []string{"--", invalid})
				fixture.assertOnePicker(t, "cp")
				fixture.assertCPArgs(t, []string{"--", invalid})
			})
		}
	})
}

func newZshAdapterFixture(t *testing.T, zsh, plugin string) zshAdapterFixture {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	home := filepath.Join(root, "home 'quoted\u00a0trail ")
	cwd := filepath.Join(root, "cwd\\line\nwith\ttabs")
	tmp := filepath.Join(root, "tmp")
	for _, directory := range []string{bin, home, cwd, tmp, filepath.Join(home, ".config", "fzf")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fixture := zshAdapterFixture{
		zsh: zsh, plugin: plugin, runner: filepath.Join(root, "runner.zsh"), cwd: cwd, home: home,
		output: filepath.Join(root, "output.nul"), state: filepath.Join(root, "state.nul"),
		calls: filepath.Join(root, "calls.nul"), cpCalls: filepath.Join(root, "cp-calls.nul"),
		unexpected: filepath.Join(root, "unexpected"),
	}
	writeExecutable(t, filepath.Join(bin, "shell-picker"), zsh, fakeShellPicker)
	writeExecutable(t, filepath.Join(bin, "cp"), zsh, fakeCP)
	writeExecutable(t, filepath.Join(bin, "fzf"), zsh, unexpectedExecutable)
	writeExecutable(t, filepath.Join(home, ".config", "fzf", "fzf-picker-candidates.zsh"), zsh, unexpectedExecutable)
	if err := os.WriteFile(fixture.runner, []byte(zshAdapterRunner), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.environment = replaceEnvironment(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		"TMPDIR="+tmp,
		"SHELL_PICKER_CALLS="+fixture.calls,
		"SHELL_PICKER_CP_CALLS="+fixture.cpCalls,
		"SHELL_PICKER_OUTPUT="+fixture.output,
		"SHELL_PICKER_STATE="+fixture.state,
		"SHELL_PICKER_UNEXPECTED="+fixture.unexpected,
	)
	return fixture
}

func (fixture zshAdapterFixture) run(t *testing.T, scenario string, records []string) []string {
	return fixture.runLocale(t, scenario, records, "")
}

func (fixture zshAdapterFixture) runCP(t *testing.T, lbuffer string, records []string) []string {
	fixture.environment = replaceEnvironment(fixture.environment, "SHELL_PICKER_LBUFFER="+lbuffer)
	return fixture.run(t, "tab-custom", records)
}

func (fixture zshAdapterFixture) runCPHeredoc(t *testing.T, lbuffer string, records []string) []string {
	fixture.environment = replaceEnvironment(fixture.environment, "SHELL_PICKER_LBUFFER="+lbuffer)
	return fixture.run(t, "tab-heredoc", records)
}

func (fixture zshAdapterFixture) runLocale(t *testing.T, scenario string, records []string, locale string) []string {
	t.Helper()
	var output []byte
	for _, record := range records {
		output = append(output, record...)
		output = append(output, 0)
	}
	if err := os.WriteFile(fixture.output, output, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fixture.state, fixture.calls, fixture.cpCalls, fixture.unexpected} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	command := exec.Command(fixture.zsh, "-f", fixture.runner, fixture.plugin, fixture.cwd, scenario)
	command.Dir = fixture.cwd
	command.Env = fixture.environment
	if locale != "" {
		command.Env = replaceEnvironment(command.Env, "LC_ALL="+locale, "LANG="+locale)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("zsh scenario %s: %v\n%s", scenario, err, output)
	}
	return readNULRecords(t, fixture.state)
}

func findZshUTF8Locale(zsh string) (string, bool) {
	candidates := []string{"C.UTF-8", "C.utf8", "en_US.UTF-8", "en_US.utf8", "UTF-8"}
	if locale, err := exec.LookPath("locale"); err == nil {
		if output, err := exec.Command(locale, "-a").Output(); err == nil {
			candidates = append(candidates, strings.Fields(string(output))...)
		}
	}
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		command := exec.Command(zsh, "-fc", `zmodload zsh/langinfo || exit 1
codeset=${langinfo[CODESET]:l}
[[ $codeset == utf-8 || $codeset == utf8 ]]`)
		command.Env = replaceEnvironment(os.Environ(), "LC_ALL="+candidate, "LANG="+candidate)
		if command.Run() == nil {
			return candidate, true
		}
	}
	return "", false
}

func TestReplaceEnvironmentReplacesCaseVariantOnWindows(t *testing.T) {
	environment := []string{"Path=old-tools", "KEEP=yes"}
	got := replaceEnvironment(environment, "PATH=new-tools")
	if runtime.GOOS == "windows" {
		if !reflect.DeepEqual(got, []string{"KEEP=yes", "PATH=new-tools"}) {
			t.Fatalf("case-insensitive environment replacement=%q", got)
		}
		return
	}
	if !reflect.DeepEqual(got, []string{"Path=old-tools", "KEEP=yes", "PATH=new-tools"}) {
		t.Fatalf("case-sensitive environment replacement=%q", got)
	}
}

func replaceEnvironment(environment []string, replacements ...string) []string {
	keyName := func(key string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}
	keys := make(map[string]bool, len(replacements))
	for _, replacement := range replacements {
		key, _, _ := strings.Cut(replacement, "=")
		keys[keyName(key)] = true
	}
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !keys[keyName(key)] {
			result = append(result, entry)
		}
	}
	return append(result, replacements...)
}

func (fixture zshAdapterFixture) assertCPArgs(t *testing.T, want []string) {
	t.Helper()
	if got := readNULRecords(t, fixture.cpCalls); !reflect.DeepEqual(got, want) {
		t.Fatalf("executed cp argv=%q, want %q", got, want)
	}
}

func (fixture zshAdapterFixture) assertOnePicker(t *testing.T, operation string) {
	t.Helper()
	records := readNULRecords(t, fixture.calls)
	if len(records) != 8 {
		t.Fatalf("picker process records=%d (%q), want one PID and seven arguments", len(records), records)
	}
	if pid, err := strconv.Atoi(records[0]); err != nil || pid <= 0 {
		t.Fatalf("picker PID=%q", records[0])
	}
	want := []string{operation, "--cwd", fixture.cwd, "--home", fixture.home, "--output", "nul"}
	if !reflect.DeepEqual(records[1:], want) {
		t.Fatalf("picker argv=%q, want %q", records[1:], want)
	}
	if unexpected, err := os.ReadFile(fixture.unexpected); err == nil {
		t.Fatalf("adapter launched helper/fzf subprocess: %q", unexpected)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func assertZshAdapterState(t *testing.T, state []string, accepted, completion int, wantPaths []string) {
	t.Helper()
	if len(state) < 5 {
		t.Fatalf("state records=%q", state)
	}
	if state[0] != strconv.Itoa(accepted) || state[1] != strconv.Itoa(completion) {
		t.Fatalf("accepted/completion=%q/%q, want %d/%d", state[0], state[1], accepted, completion)
	}
	if !reflect.DeepEqual(state[5:], wantPaths) {
		t.Fatalf("decoded editor paths=%q, want %q", state[5:], wantPaths)
	}
}

func readNULRecords(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(raw, []byte{0})
	if len(parts) == 1 || len(parts[len(parts)-1]) != 0 {
		t.Fatalf("%s is not exactly NUL framed: %q", path, raw)
	}
	parts = parts[:len(parts)-1]
	records := make([]string, len(parts))
	for index := range parts {
		records[index] = string(parts[index])
	}
	return records
}

func writeExecutable(t *testing.T, path, zsh, body string) {
	t.Helper()
	content := fmt.Sprintf("#!%s\n%s", zsh, body)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

const fakeShellPicker = `emulate -LR zsh
emit() { print -rn -- "$1"$'\0' }
{
  emit "$$"
  for argument in "$@"; do emit "$argument"; done
} >> "$SHELL_PICKER_CALLS"
while IFS= read -r -d $'\0' record; do emit "$record"; done < "$SHELL_PICKER_OUTPUT"
`

const fakeCP = `emulate -LR zsh
for argument in "$@"; do print -rn -- "$argument"$'\0'; done >| "$SHELL_PICKER_CP_CALLS"
`

const unexpectedExecutable = `emulate -LR zsh
print -r -- "${0:t}" >> "$SHELL_PICKER_UNEXPECTED"
exit 97
`

const zshAdapterRunner = `emulate -LR zsh
typeset -gi accepted_calls=0 fzf_completion_calls=0
zle() {
  case $1 in
    -N | redisplay) ;;
    magic-space)
      BUFFER="${BUFFER[1,CURSOR]} ${BUFFER[$(( CURSOR + 1 )),-1]}"
      (( ++CURSOR ))
      ;;
    accept-line) (( ++accepted_calls )) ;;
    fzf_completion) (( ++fzf_completion_calls )) ;;
    _shell_picker_cd | _shell_picker_cp) "$1" ;;
    *) return 90 ;;
  esac
}
bindkey() { return 0 }
source "$1" || exit $?
cd -- "$2" || exit $?
typeset -a decoded
case $3 in
  cd)
    BUFFER='original cd state' CURSOR=4
    _shell_picker_cd
    quoted=${BUFFER#'builtin cd -- '}
    ;;
  space)
    BUFFER=cd CURSOR=2
    _shell_picker_space
    quoted=${BUFFER#'builtin cd -- '}
    ;;
  tab | tab-options | tab-terminator | tab-escaped-terminator | tab-custom | tab-heredoc)
    case $3 in
      tab) LBUFFER='cp ' ;;
      tab-options) LBUFFER='cp -a existing ' ;;
      tab-terminator) LBUFFER='cp -a -- existing ' ;;
      tab-escaped-terminator) LBUFFER='cp -a \-- existing ' ;;
      tab-custom) LBUFFER=$SHELL_PICKER_LBUFFER ;;
      tab-heredoc) LBUFFER=$SHELL_PICKER_LBUFFER ;;
    esac
    RBUFFER=
    _shell_picker_tab
    [[ $LBUFFER != *' ' ]] || exit 91
    if [[ $3 == tab-heredoc ]]; then
      eval "$LBUFFER"$'\nheredoc body\n--\n' || exit 94
    else
      eval "$LBUFFER" || exit 94
    fi
    while IFS= read -r -d $'\0' argument; do decoded+=("$argument"); done < "$SHELL_PICKER_CP_CALLS"
    ;;
  *) exit 92 ;;
esac
if [[ $3 == cd || $3 == space ]]; then
  eval "decoded=( $quoted )" || exit 93
fi
emit() { print -rn -- "$1"$'\0' }
{
  emit "$accepted_calls"
  emit "$fzf_completion_calls"
  emit "$BUFFER"
  emit "$LBUFFER"
  emit "$RBUFFER"
  for path in "${decoded[@]}"; do emit "$path"; done
} >| "$SHELL_PICKER_STATE"
`
