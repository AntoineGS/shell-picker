//go:build linux

package integration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestWindowsTracePipeSourceUsesRandomRestrictedFirstInstance(t *testing.T) {
	raw, err := os.ReadFile("fzf_real_windows_test.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{"rand.Read", "FILE_FLAG_FIRST_PIPE_INSTANCE", "FILE_FLAG_OVERLAPPED", "currentUserSecurityAttributes"} {
		if !strings.Contains(source, required) {
			t.Errorf("Windows trace pipe source lacks %s", required)
		}
	}
	if strings.Contains(source, "os.Getpid()") || strings.Contains(source, "t.Name()") {
		t.Error("Windows trace pipe name uses predictable process/test identity")
	}
}

func TestWindowsRegistersNativePreviewLifecycleTests(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "fzf_real_preview_windows_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"TestRealFZFPreviewReplacementKillsWholeTree":     false,
		"TestRealFZFResizeUpdatesPreviewDimensions":       false,
		"TestRealFZFPreviewTerminalFailuresKillWholeTree": false,
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil {
			if _, exists := want[function.Name.Name]; exists {
				want[function.Name.Name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("Windows native test %s is not registered", name)
		}
	}
}

func TestWindowsOutputDrainUsesCancellableOverlappedIO(t *testing.T) {
	raw, err := os.ReadFile("fzf_real_windows_test.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (session *windowsTerminalSession) drainOutput")
	if start < 0 {
		t.Fatal("cannot locate Windows output drain")
	}
	end := strings.Index(source[start:], "func (session *windowsTerminalSession) waitProcess")
	if end < 0 {
		t.Fatal("cannot locate end of Windows output drain")
	}
	body := source[start : start+end]
	if !strings.Contains(body, "windows.Overlapped") || !strings.Contains(body, "GetOverlappedResult") {
		t.Fatal("Windows output drain is not overlapped/cancellable")
	}
}

func TestWindowsTerminalLifecycleSourceContract(t *testing.T) {
	raw, err := os.ReadFile("fzf_real_windows_test.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{"waitErr", "waitDone", "DuplicateHandle", "ops.closeHandle(information.Thread)",
		"ops.cancelIO(session.output", "ops.cancelIO(session.trace", "<-session.drainDone", "<-session.traceDone"} {
		if !strings.Contains(source, required) {
			t.Errorf("Windows lifecycle source lacks %s", required)
		}
	}
	if strings.Contains(source, "waitResult") || strings.Contains(source, "session.thread") {
		t.Error("Windows lifecycle retains consumable wait or thread-handle state")
	}
	if strings.Contains(source, "windowsHandleOwner") || strings.Contains(source, "windowsWaitState") {
		t.Error("Windows lifecycle retains a parallel state model")
	}
	traceCreate := strings.Index(source, "createWindowsTerminalResources(config, defaultWindowsTerminalFactory())")
	outputDrain := strings.Index(source, "go session.drainOutput")
	if traceCreate < 0 || outputDrain < 0 || traceCreate > outputDrain {
		t.Error("Windows constructor starts output drain before fallible trace resource setup")
	}
}

func TestWindowsPreviewTestsRequireTopologyAndCheckOperations(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "fzf_real_preview_windows_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"TestRealFZFPreviewReplacementKillsWholeTree":     false,
		"TestRealFZFResizeUpdatesPreviewDimensions":       false,
		"TestRealFZFPreviewTerminalFailuresKillWholeTree": false,
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		if _, exists := wanted[function.Name.Name]; !exists {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, selected := call.Fun.(*ast.SelectorExpr)
			if selected && selector.Sel.Name == "AssertProcessTopology" {
				wanted[function.Name.Name] = true
			}
			return true
		})
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("%s lacks AssertProcessTopology", name)
		}
	}
	raw, err := os.ReadFile("fzf_real_preview_windows_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"_ = term.Send", "_ = term.Resize", "_ = f.controller.release", "_ = term.Wait"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("Windows preview source ignores operation error with %q", forbidden)
		}
	}
}

func TestResizeAndFinishedEvidenceSourceOrderingMatchesWindows(t *testing.T) {
	for _, path := range []string{"fzf_real_preview_linux_test.go", "fzf_real_preview_windows_test.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		start := strings.Index(source, "func TestRealFZFResizeUpdatesPreviewDimensions")
		if start < 0 {
			t.Fatalf("%s lacks resize test boundary", path)
		}
		end := strings.Index(source[start:], "func TestRealFZFPreviewTerminalFailuresKillWholeTree")
		if end < 0 {
			t.Fatalf("%s lacks resize/failure test boundaries", path)
		}
		body := source[start : start+end]
		for _, required := range []string{
			"Resize(101, 37)", `Operation: "es", Count: 1`, "Send(keyDown)", `Renderer: "chafa", Operation: "ok", Count: 2`,
			"Resize(83, 29)", `Operation: "es", Count: 2`, "Send([]byte{0x1b, '[', 'A'})",
			`Renderer: "chafa", Operation: "ok", Count: 3`, `Event: "session.close"`, "assertFinishedTrace",
		} {
			if !strings.Contains(body, required) {
				t.Errorf("%s resize evidence lacks %s", path, required)
			}
		}
		closeBarrier := strings.LastIndex(body, `Event: "session.close"`)
		finishedAssertion := strings.LastIndex(body, "assertFinishedTrace")
		if closeBarrier < 0 || finishedAssertion < closeBarrier {
			t.Errorf("%s samples finished telemetry before session.close synchronization", path)
		}
	}
}
