package integration

import (
	"encoding/json"
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
	if os.Getenv(parityHelperEnvironment) != "" {
		os.Exit(runParityHelper())
	}
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
	Buffer                     string   `json:"buffer"`
	Cursor                     int      `json:"cursor"`
	PWD                        string   `json:"pwd"`
	Home                       string   `json:"home"`
	Edits                      []string `json:"edits"`
	Decoded                    []string `json:"decoded"`
	TabBefore                  string   `json:"tab_before"`
	TabAfter                   string   `json:"tab_after"`
	ControlSpaceBefore         string   `json:"control_space_before"`
	ControlSpaceAfter          string   `json:"control_space_after"`
	UnrelatedBefore            string   `json:"unrelated_before"`
	UnrelatedAfter             string   `json:"unrelated_after"`
	FirstBindings              string   `json:"first_bindings"`
	SecondBindings             string   `json:"second_bindings"`
	PostRestoreBindings        string   `json:"post_restore_bindings"`
	OwnCount                   int      `json:"own_count"`
	OwnAfterSetup              int      `json:"own_after_setup"`
	FirstClosureCount          int      `json:"first_closure_count"`
	FirstRestoreCount          int      `json:"first_restore_count"`
	SecondClosureCount         int      `json:"second_closure_count"`
	SecondRestoreCount         int      `json:"second_restore_count"`
	FirstHookOrder             []string `json:"first_hook_order"`
	SecondHookOrder            []string `json:"second_hook_order"`
	SuspendedHookOrder         []string `json:"suspended_hook_order"`
	FirstResumeHookOrder       []string `json:"first_resume_hook_order"`
	SecondResumeHookOrder      []string `json:"second_resume_hook_order"`
	PostRestoreHookOrder       []string `json:"post_restore_hook_order"`
	FirstClosureResults        []string `json:"first_closure_results"`
	SecondClosureResults       []string `json:"second_closure_results"`
	SuspendedClosureResults    []string `json:"suspended_closure_results"`
	FirstResumeClosureResults  []string `json:"first_resume_closure_results"`
	SecondResumeClosureResults []string `json:"second_resume_closure_results"`
	PostRestoreClosureResults  []string `json:"post_restore_closure_results"`
	OwnAfterSpace              int      `json:"own_after_space"`
	OwnAfterFirstHook          int      `json:"own_after_first_hook"`
	OwnAfterSecondHook         int      `json:"own_after_second_hook"`
	OwnAfterNewPrompt          int      `json:"own_after_new_prompt"`
	BufferAfterSpace           string   `json:"buffer_after_space"`
	BufferAfterNewPrompt       string   `json:"buffer_after_new_prompt"`
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

	t.Run("spawn-failures-are-soft", func(t *testing.T) {
		emptyBin := filepath.Join(fixture.root, "empty picker bin")
		unlaunchableBin := filepath.Join(fixture.root, "unlaunchable picker bin")
		for _, directory := range []string{emptyBin, unlaunchableBin} {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		unlaunchable := filepath.Join(unlaunchableBin, "shell-picker")
		if runtime.GOOS == "windows" {
			unlaunchable += ".exe"
		}
		if err := os.WriteFile(unlaunchable, []byte("not an executable"), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, spawnCase := range []struct {
			name string
			path string
		}{{"absent", emptyBin}, {"unlaunchable", unlaunchableBin}} {
			for _, operation := range []string{"cd", "cp"} {
				t.Run(operation+"-"+spawnCase.name, func(t *testing.T) {
					spawnFixture := fixture
					spawnFixture.environment = replaceEnvironment(fixture.environment, "PATH="+spawnCase.path)
					state := spawnFixture.run(t, operation+"-"+spawnCase.name, operation, 2, nil, 0, false)
					if state.Buffer != operation+" " || filepath.Clean(state.PWD) != filepath.Clean(fixture.cwd) {
						t.Fatalf("spawn failure changed buffer/PWD: %q/%q", state.Buffer, state.PWD)
					}
					if !reflect.DeepEqual(state.Edits, []string{"insert"}) {
						t.Fatalf("spawn failure reached a replacement edit: %q", state.Edits)
					}
					fixture.assertNoPicker(t)
				})
			}
		}
	})

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
		if state.FirstBindings != state.SecondBindings || state.OwnAfterSetup != 1 || state.OwnCount != 1 {
			t.Fatalf("binding is not idempotent: own=%d", state.OwnCount)
		}
		if state.FirstBindings != state.PostRestoreBindings {
			t.Fatalf("restoring Space changed bindings: first=%q restored=%q", state.FirstBindings, state.PostRestoreBindings)
		}
		if state.FirstClosureCount != 1 || state.FirstRestoreCount != 1 || state.SecondClosureCount != 1 || state.SecondRestoreCount != 1 {
			t.Fatalf("pre_prompt hooks were not preserved and idempotently configured: %+v", state)
		}
		wantHookOrder := []string{"closure", "string:unrelated_pre_prompt", "string:_shell_picker_restore_space_binding"}
		wantClosureResults := []string{"retained closure"}
		if !reflect.DeepEqual(state.FirstHookOrder, wantHookOrder) || !reflect.DeepEqual(state.SecondHookOrder, wantHookOrder) || !reflect.DeepEqual(state.SuspendedHookOrder, wantHookOrder) || !reflect.DeepEqual(state.FirstResumeHookOrder, wantHookOrder) || !reflect.DeepEqual(state.SecondResumeHookOrder, wantHookOrder) || !reflect.DeepEqual(state.PostRestoreHookOrder, wantHookOrder) || !reflect.DeepEqual(state.FirstClosureResults, wantClosureResults) || !reflect.DeepEqual(state.SecondClosureResults, wantClosureResults) || !reflect.DeepEqual(state.SuspendedClosureResults, wantClosureResults) || !reflect.DeepEqual(state.FirstResumeClosureResults, wantClosureResults) || !reflect.DeepEqual(state.SecondResumeClosureResults, wantClosureResults) || !reflect.DeepEqual(state.PostRestoreClosureResults, wantClosureResults) {
			t.Fatalf("pre_prompt hook order or closure behavior changed: %+v", state)
		}
		if state.BufferAfterSpace != "curl.exe " || state.OwnAfterSpace != 0 || state.OwnAfterFirstHook != 0 || state.OwnAfterSecondHook != 0 || state.BufferAfterNewPrompt != "" || state.OwnAfterNewPrompt != 1 {
			t.Fatalf("ordinary Space did not suspend and restore its binding: %+v", state)
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
