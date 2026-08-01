package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func newNushellFixture(t *testing.T, nu, repository string) nushellFixture {
	t.Helper()
	base := filepath.Join(repository, ".git", "test-tmp")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "nushell-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove Nushell fixture: %v", err)
		}
	})
	bin := filepath.Join(root, "bin")
	home := filepath.Join(root, "home with space")
	cwd := filepath.Join(root, "cwd with space")
	for _, directory := range []string{bin, home, cwd} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	picker := filepath.Join(bin, "shell-picker")
	if runtime.GOOS == "windows" {
		picker += ".exe"
	}
	copyNushellExecutable(t, self, picker)

	adapter := filepath.Join(repository, "adapters", "nushell", "shell-picker.nu")
	runner := filepath.Join(root, "runner.nu")
	quotedAdapter, err := json.Marshal(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner, []byte(strings.Replace(nushellRunner, "__ADAPTER__", string(quotedAdapter), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := nushellFixture{
		nu: nu, root: root, runner: runner, cwd: cwd, home: home,
		output: filepath.Join(root, "output.nuon"), emitted: filepath.Join(root, "emitted.nuon"),
		calls: filepath.Join(root, "calls.jsonl"), state: filepath.Join(root, "state.json"),
	}
	fixture.environment = replaceEnvironment(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home, "USERPROFILE="+home, "TMPDIR="+root, "TEMP="+root, "TMP="+root,
		nushellPickerHelper+"=1", "SHELL_PICKER_NUSHELL_CALLS="+fixture.calls,
		"SHELL_PICKER_NUSHELL_OUTPUT="+fixture.output, "SHELL_PICKER_NUSHELL_EMITTED="+fixture.emitted,
		"SHELL_PICKER_NUSHELL_STATE="+fixture.state,
	)
	return fixture
}

func copyNushellExecutable(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func (fixture nushellFixture) run(t *testing.T, scenario, buffer string, cursor int, output []byte, exitCode int, decode bool) nushellState {
	t.Helper()
	if err := os.WriteFile(fixture.output, output, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fixture.calls, fixture.emitted, fixture.state} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	decodeValue := "0"
	if decode {
		decodeValue = "1"
	}
	command := exec.Command(fixture.nu, "--no-config-file", fixture.runner)
	command.Dir = fixture.cwd
	command.Env = replaceEnvironment(fixture.environment,
		"SHELL_PICKER_NUSHELL_SCENARIO="+scenario, "SHELL_PICKER_NUSHELL_BUFFER="+buffer,
		"SHELL_PICKER_NUSHELL_CURSOR="+strconv.Itoa(cursor), "SHELL_PICKER_NUSHELL_EXIT="+strconv.Itoa(exitCode),
		"SHELL_PICKER_NUSHELL_DECODE="+decodeValue,
	)
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Nushell scenario %s: %v\n%s", scenario, err, combined)
	}
	if len(combined) != 0 {
		t.Fatalf("Nushell scenario %s leaked host output: %q", scenario, combined)
	}
	var state nushellState
	raw, err := os.ReadFile(fixture.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode state %q: %v", raw, err)
	}
	return state
}

func (fixture nushellFixture) runBinding(t *testing.T) nushellState {
	t.Helper()
	return fixture.run(t, "binding", "", 0, nil, 0, false)
}

func (fixture nushellFixture) pickerCalls(t *testing.T) []nushellPickerCall {
	t.Helper()
	file, err := os.Open(fixture.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var calls []nushellPickerCall
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var call nushellPickerCall
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return calls
}

func (fixture nushellFixture) assertOnePicker(t *testing.T, operation, home string) {
	t.Helper()
	if runtime.GOOS != "windows" && home != fixture.home {
		t.Fatalf("Nushell home=%q, want isolated home %q", home, fixture.home)
	}
	calls := fixture.pickerCalls(t)
	if len(calls) != 1 || calls[0].PID <= 0 {
		t.Fatalf("picker calls=%+v, want one process", calls)
	}
	want := []string{operation, "--cwd", fixture.cwd, "--home", home, "--output", "nuon"}
	if !reflect.DeepEqual(calls[0].Args, want) || filepath.Clean(calls[0].PWD) != filepath.Clean(fixture.cwd) {
		t.Fatalf("picker argv/PWD=%q/%q, want %q/%q", calls[0].Args, calls[0].PWD, want, fixture.cwd)
	}
	wantOutput, err := os.ReadFile(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	emitted, err := os.ReadFile(fixture.emitted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(emitted, wantOutput) {
		t.Fatalf("fake emitted bytes=%q, want exact %q", emitted, wantOutput)
	}
}

func (fixture nushellFixture) assertNoPicker(t *testing.T) {
	t.Helper()
	if calls := fixture.pickerCalls(t); len(calls) != 0 {
		t.Fatalf("ordinary Space invoked picker: %+v", calls)
	}
}

func testNushellWindowsDrive(t *testing.T, fixture nushellFixture) {
	t.Helper()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal("powershell.exe unavailable for GetLogicalDrives query")
	}
	query := `[Environment]::GetLogicalDrives() | ForEach-Object { $_.Substring(0, 2).ToUpperInvariant() }`
	output, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", query).CombinedOutput()
	if err != nil {
		t.Fatalf("GetLogicalDrives query: %v\n%s", err, output)
	}
	occupied := make(map[string]bool)
	for _, volume := range strings.Fields(string(output)) {
		occupied[strings.ToUpper(volume)] = true
	}
	startVolume := strings.ToUpper(filepath.VolumeName(fixture.cwd))
	drive := ""
	for letter := 'Z'; letter >= 'D'; letter-- {
		candidate := string(letter) + ":"
		if candidate != startVolume && !occupied[candidate] {
			drive = candidate
			break
		}
	}
	if drive == "" {
		t.Fatal("no unused substitute drive available from Z: through D:")
	}
	mappedRoot := filepath.Join(fixture.root, "substitute drive path with spaces")
	if err := os.MkdirAll(mappedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("subst", drive, mappedRoot).CombinedOutput(); err != nil {
		t.Fatalf("subst %s: %v\n%s", drive, err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("subst", drive, "/D").CombinedOutput(); err != nil {
			t.Errorf("cleanup subst %s: %v\n%s", drive, err, output)
		}
	})
	target := drive + `\native backslash path\child with space`
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	state := fixture.run(t, "windows-cd", "cd", 2, acceptedNUON(t, []string{target}), 0, false)
	if !strings.EqualFold(filepath.Clean(state.PWD), filepath.Clean(target)) || state.Buffer != "" {
		t.Fatalf("cross-volume %%cd PWD/buffer=%q/%q, want %q/empty", state.PWD, state.Buffer, target)
	}
	fixture.assertOnePicker(t, "cd", state.Home)
	paths := []string{target, drive + `\second path with space`, target}
	state = fixture.run(t, "windows-cp", "cp", 2, acceptedNUON(t, paths), 0, true)
	want := `cp "` + drive + `\\native backslash path\\child with space" "` + drive + `\\second path with space" "` + drive + `\\native backslash path\\child with space"`
	if state.Buffer != want || !reflect.DeepEqual(state.Decoded, paths) || strings.HasSuffix(state.Buffer, " ") {
		t.Fatalf("Windows cp buffer/decoded=%q/%q, want %q/%q", state.Buffer, state.Decoded, want, paths)
	}
	fixture.assertOnePicker(t, "cp", state.Home)
}

const nushellRunner = `def commandline [] {
  $env.SHELL_PICKER_TEST_BUFFER
}

def "commandline get-cursor" [] {
  $env.SHELL_PICKER_TEST_CURSOR
}

def --env "commandline edit" [text: string, --append, --insert, --replace] {
  let operation = if $insert { "insert" } else if $append { "append" } else { "replace" }
  $env.SHELL_PICKER_TEST_EDITS = ($env.SHELL_PICKER_TEST_EDITS | append $operation)
  if $insert {
    let cursor = $env.SHELL_PICKER_TEST_CURSOR
    let buffer = ($env.SHELL_PICKER_TEST_BUFFER | encode utf-8)
    let left = ($buffer | bytes at 0..<$cursor | decode utf-8)
    let right = ($buffer | bytes at $cursor.. | decode utf-8)
    $env.SHELL_PICKER_TEST_BUFFER = $left + $text + $right
    $env.SHELL_PICKER_TEST_CURSOR = $cursor + ($text | encode utf-8 | bytes length)
  } else {
    $env.SHELL_PICKER_TEST_BUFFER = $text
    $env.SHELL_PICKER_TEST_CURSOR = ($text | encode utf-8 | bytes length)
  }
}

source __ADAPTER__

$env.SHELL_PICKER_TEST_BUFFER = $env.SHELL_PICKER_NUSHELL_BUFFER
$env.SHELL_PICKER_TEST_CURSOR = ($env.SHELL_PICKER_NUSHELL_CURSOR | into int)
$env.SHELL_PICKER_TEST_EDITS = []

let state = if $env.SHELL_PICKER_NUSHELL_SCENARIO == "binding" {
  let tab = {name: completion_menu, modifier: none, keycode: tab, mode: [emacs vi_normal vi_insert], event: {until: [{send: menu, name: completion_menu}, {edit: complete}]}}
  let control_space = {name: ide_completion_menu, modifier: control, keycode: space, mode: [emacs vi_normal vi_insert], event: {send: menu, name: ide_completion_menu}}
  let unrelated = {name: unrelated, modifier: shift, keycode: char_x, mode: emacs, event: {edit: undo}}
  let old_one = {name: _shell_picker_space, modifier: none, keycode: space, mode: emacs, event: {edit: complete}}
  let old_two = {name: _shell_picker_space, modifier: shift, keycode: space, mode: vi_insert, event: {edit: undo}}
  $env.config = {keybindings: [$tab $control_space $unrelated $old_one $old_two]}
  let tab_before = (($env.config.keybindings | where keycode == tab | first) | to nuon --raw)
  let control_space_before = (($env.config.keybindings | where name == ide_completion_menu | first) | to nuon --raw)
  let unrelated_before = (($env.config.keybindings | where name == unrelated | first) | to nuon --raw)
  shell-picker-bind-nushell
  let first = ($env.config.keybindings | to nuon --raw)
  shell-picker-bind-nushell
  {
    buffer: "", cursor: 0, pwd: $env.PWD, home: $nu.home-dir, edits: [], decoded: [],
    tab_before: $tab_before,
    tab_after: (($env.config.keybindings | where keycode == tab | first) | to nuon --raw),
    control_space_before: $control_space_before,
    control_space_after: (($env.config.keybindings | where name == ide_completion_menu | first) | to nuon --raw),
    unrelated_before: $unrelated_before,
    unrelated_after: (($env.config.keybindings | where name == unrelated | first) | to nuon --raw),
    first_bindings: $first,
    second_bindings: ($env.config.keybindings | to nuon --raw),
    own_count: (($env.config.keybindings | where name == _shell_picker_space) | length),
  }
} else {
  _shell_picker_space
  let decoded = if $env.SHELL_PICKER_NUSHELL_DECODE == "1" {
    let encoded = ($env.SHELL_PICKER_TEST_BUFFER | str substring 3..)
    $"[($encoded)]" | from nuon
  } else {
    []
  }
  {
    buffer: $env.SHELL_PICKER_TEST_BUFFER,
    cursor: $env.SHELL_PICKER_TEST_CURSOR,
    pwd: $env.PWD,
    home: $nu.home-dir,
    edits: $env.SHELL_PICKER_TEST_EDITS,
    decoded: $decoded,
    tab_before: "", tab_after: "", control_space_before: "", control_space_after: "",
    unrelated_before: "", unrelated_after: "", first_bindings: "", second_bindings: "", own_count: 0,
  }
}

$state | to json | save --force $env.SHELL_PICKER_NUSHELL_STATE
`
