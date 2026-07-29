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

const nushellPickerHelper = "SHELL_PICKER_NUSHELL_GO_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(nushellPickerHelper) == "1" {
		os.Exit(runNushellPickerFake(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type nushellPickerCall struct {
	PID  int      `json:"pid"`
	Args []string `json:"args"`
	PWD  string   `json:"pwd"`
}

func runNushellPickerFake(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		return 120
	}
	log, err := os.OpenFile(os.Getenv("SHELL_PICKER_NUSHELL_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 121
	}
	if err := json.NewEncoder(log).Encode(nushellPickerCall{PID: os.Getpid(), Args: args, PWD: cwd}); err != nil {
		_ = log.Close()
		return 122
	}
	if err := log.Close(); err != nil {
		return 123
	}
	raw, err := os.ReadFile(os.Getenv("SHELL_PICKER_NUSHELL_OUTPUT"))
	if err != nil {
		return 124
	}
	if err := os.WriteFile(os.Getenv("SHELL_PICKER_NUSHELL_EMITTED"), raw, 0o600); err != nil {
		return 125
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		return 126
	}
	code, err := strconv.Atoi(os.Getenv("SHELL_PICKER_NUSHELL_EXIT"))
	if err != nil {
		return 127
	}
	return code
}

type nushellState struct {
	Buffer             string   `json:"buffer"`
	Cursor             int      `json:"cursor"`
	PWD                string   `json:"pwd"`
	Home               string   `json:"home"`
	Edits              []string `json:"edits"`
	Decoded            []string `json:"decoded"`
	TabBefore          string   `json:"tab_before"`
	TabAfter           string   `json:"tab_after"`
	ControlSpaceBefore string   `json:"control_space_before"`
	ControlSpaceAfter  string   `json:"control_space_after"`
	UnrelatedBefore    string   `json:"unrelated_before"`
	UnrelatedAfter     string   `json:"unrelated_after"`
	FirstBindings      string   `json:"first_bindings"`
	SecondBindings     string   `json:"second_bindings"`
	OwnCount           int      `json:"own_count"`
}

type nushellFixture struct {
	nu          string
	root        string
	runner      string
	cwd         string
	home        string
	output      string
	emitted     string
	calls       string
	state       string
	environment []string
}

func TestNushellAdapter(t *testing.T) {
	nu, err := exec.LookPath("nu")
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Fatal("nu is unavailable on Windows")
		}
		t.Skip("nu is unavailable")
	}
	nu, err = filepath.Abs(nu)
	if err != nil {
		t.Fatal(err)
	}
	versionOutput, err := exec.Command(nu, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("nu --version: %v\n%s", err, versionOutput)
	}
	version := strings.TrimSpace(string(versionOutput))
	if !nushellVersionSupported(version) {
		t.Fatalf("Nushell 0.113.1+ required, got %q", version)
	}

	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	testTemp := filepath.Join(repository, ".git", "test-tmp")

	t.Run("direct-suite", func(t *testing.T) {
		command := exec.Command(nu, "--no-config-file", filepath.Join(repository, "adapters", "nushell", "shell-picker.test.nu"))
		command.Dir = repository
		command.Env = replaceEnvironment(os.Environ(), "TMPDIR="+testTemp, "TEMP="+testTemp, "TMP="+testTemp)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("direct Nushell suite: %v\n%s", err, output)
		}
		if string(output) != "nushell adapter tests: PASS\n" {
			t.Fatalf("direct Nushell suite output=%q", output)
		}
	})

	fixture := newNushellFixture(t, nu, repository)
	routes := []struct {
		name, buffer, want, operation string
		cursor                        int
	}{
		{"cd-exact", "cd", "cd ", "cd", 2},
		{"cp-exact", "cp", "cp ", "cp", 2},
		{"leading", " cd", " cd ", "", 3},
		{"suffix", "cd x", "cd x ", "", 4},
		{"cursor-one", "cp", "c p", "", 1},
		{"utf8-byte-cursor", "écd", "é cd", "", 2},
	}
	for _, route := range routes {
		t.Run("route-"+route.name, func(t *testing.T) {
			state := fixture.run(t, route.name, route.buffer, route.cursor, []byte(`{status:cancelled,paths:[]}`), 0, false)
			if state.Buffer != route.want || !reflect.DeepEqual(state.Edits, []string{"insert"}) {
				t.Fatalf("buffer/edits=%q/%q, want %q/[insert]", state.Buffer, state.Edits, route.want)
			}
			if state.Cursor != route.cursor+1 {
				t.Fatalf("cursor=%d, want byte cursor %d", state.Cursor, route.cursor+1)
			}
			if route.operation == "" {
				fixture.assertNoPicker(t)
			} else {
				fixture.assertOnePicker(t, route.operation, state.Home)
			}
		})
	}

	t.Run("cd-accepted", func(t *testing.T) {
		target := filepath.Join(fixture.root, "selected cd with space")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		state := fixture.run(t, "cd-accepted", "cd", 2, acceptedNUON(t, []string{target}), 0, false)
		if filepath.Clean(state.PWD) != filepath.Clean(target) || state.Buffer != "" {
			t.Fatalf("PWD/buffer=%q/%q, want %q/empty", state.PWD, state.Buffer, target)
		}
		if !reflect.DeepEqual(state.Edits, []string{"insert", "replace"}) {
			t.Fatalf("cd edits=%q", state.Edits)
		}
		fixture.assertOnePicker(t, "cd", state.Home)
	})

	t.Run("cp-accepted-order-duplicates", func(t *testing.T) {
		paths := []string{"first path", "quote\"path", "line\npath", "unicodé path", "first path", `back\slash`}
		state := fixture.run(t, "cp-accepted", "cp", 2, acceptedNUON(t, paths), 0, true)
		want := "cp \"first path\" \"quote\\\"path\" \"line\npath\" \"unicodé path\" \"first path\" \"back\\\\slash\""
		if state.Buffer != want || strings.HasSuffix(state.Buffer, " ") {
			t.Fatalf("cp buffer=%q, want %q without trailing space", state.Buffer, want)
		}
		if !reflect.DeepEqual(state.Decoded, paths) || !reflect.DeepEqual(state.Edits, []string{"insert", "replace"}) {
			t.Fatalf("cp decoded/edits=%q/%q, want %q/[insert replace]", state.Decoded, state.Edits, paths)
		}
		fixture.assertOnePicker(t, "cp", state.Home)
	})

	malformed := []struct {
		name string
		raw  []byte
		code int
	}{
		{"abort", []byte(`{status:cancelled,paths:[]}`), 0},
		{"nonzero", []byte(`{status:accepted,paths:["ignored"]}`), 9},
		{"invalid", []byte(`{`), 0},
		{"scalar", []byte(`42`), 0},
		{"missing-status", []byte(`{paths:["ignored"]}`), 0},
		{"wrong-status", []byte(`{status:42,paths:["ignored"]}`), 0},
		{"missing-paths", []byte(`{status:accepted}`), 0},
		{"scalar-paths", []byte(`{status:accepted,paths:"ignored"}`), 0},
		{"null-paths", []byte(`{status:accepted,paths:null}`), 0},
		{"member-type", []byte(`{status:accepted,paths:["ok",42]}`), 0},
		{"empty", []byte(`{status:accepted,paths:[]}`), 0},
	}
	for _, operation := range []string{"cd", "cp"} {
		for _, malformedCase := range malformed {
			t.Run(operation+"-soft-"+malformedCase.name, func(t *testing.T) {
				state := fixture.run(t, operation+"-soft-"+malformedCase.name, operation, 2, malformedCase.raw, malformedCase.code, false)
				if state.Buffer != operation+" " || filepath.Clean(state.PWD) != filepath.Clean(fixture.cwd) {
					t.Fatalf("soft result changed buffer/PWD: %q/%q", state.Buffer, state.PWD)
				}
				if !reflect.DeepEqual(state.Edits, []string{"insert"}) {
					t.Fatalf("soft result reached a second edit: %q", state.Edits)
				}
				fixture.assertOnePicker(t, operation, state.Home)
			})
		}
	}

	t.Run("binding-preservation", func(t *testing.T) {
		state := fixture.runBinding(t)
		if state.TabBefore != state.TabAfter || state.ControlSpaceBefore != state.ControlSpaceAfter || state.UnrelatedBefore != state.UnrelatedAfter {
			t.Fatalf("unrelated binding bytes changed: %+v", state)
		}
		if state.FirstBindings != state.SecondBindings || state.OwnCount != 1 {
			t.Fatalf("binding is not idempotent: own=%d", state.OwnCount)
		}
		fixture.assertNoPicker(t)
	})

	if runtime.GOOS == "windows" {
		t.Run("windows-native-cross-volume", func(t *testing.T) {
			testNushellWindowsDrive(t, fixture)
		})
	}
}

func nushellVersionSupported(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	patch, errPatch := strconv.Atoi(parts[2])
	return errMajor == nil && errMinor == nil && errPatch == nil && (major > 0 || minor > 113 || minor == 113 && patch >= 1)
}

func acceptedNUON(t *testing.T, paths []string) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		Status string   `json:"status"`
		Paths  []string `json:"paths"`
	}{Status: "accepted", Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

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
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Nushell scenario %s: %v\n%s", scenario, err, combined)
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
	if home != fixture.home {
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
