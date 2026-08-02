package main

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/integration/windowsnative"
)

func TestFilteredGoEnvironmentRemovesControlledKeysCaseInsensitively(t *testing.T) {
	got := filteredGoEnvironment([]string{"Path=C:\\tools", "GOFLAGS=-exec=true", "goenv=hostile", "OTHER=value"})
	want := []string{"Path=C:\\tools", "OTHER=value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%q want=%q", got, want)
	}
}

type fakeGoCommand struct {
	listOutput      string
	listErrorOutput string
	listErr         error
	testErr         error
	calls           [][]string
	envs            [][]string
}

func (f *fakeGoCommand) run(args []string, environment []string, stdout, stderr io.Writer) error {
	f.calls = append(f.calls, append([]string(nil), args...))
	f.envs = append(f.envs, append([]string(nil), environment...))
	if len(args) < 3 {
		return errors.New("unexpected command")
	}
	if args[2] == "-list" {
		if _, err := io.WriteString(stdout, f.listOutput); err != nil {
			return err
		}
		if _, err := io.WriteString(stderr, f.listErrorOutput); err != nil {
			return err
		}
		return f.listErr
	}
	if args[2] == "-run" {
		return f.testErr
	}
	return errors.New("unexpected go test mode")
}

func TestRunManifestPreflightsAndExecutesExactCommands(t *testing.T) {
	pkg := windowsnative.Package{
		Path:    "./internal/session",
		Pattern: `^(TestAlpha|TestBeta)$`,
	}
	environment := []string{"GOENV=off", "GOFLAGS="}
	fake := &fakeGoCommand{
		listOutput: "TestAlpha\nTestBeta\nok   example.test 0.001s\n",
	}

	if err := runManifest([]windowsnative.Package{pkg}, environment, io.Discard, io.Discard, fake.run); err != nil {
		t.Fatalf("runManifest: %v", err)
	}

	wantList := []string{"test", pkg.Path, "-list", pkg.Pattern, "-count=1", "-timeout=5m"}
	wantTest := []string{"test", pkg.Path, "-run", pkg.Pattern, "-count=1", "-timeout=5m"}
	if !reflect.DeepEqual(fake.calls, [][]string{wantList, wantTest}) {
		t.Fatalf("commands=%q want=%q", fake.calls, [][]string{wantList, wantTest})
	}
	if !reflect.DeepEqual(fake.envs, [][]string{environment, environment}) {
		t.Fatalf("environments=%q want=%q", fake.envs, [][]string{environment, environment})
	}
}

func TestRunManifestRejectsInvalidListOutputAndSkipsExecution(t *testing.T) {
	pkg := windowsnative.Package{
		Path:    "./internal/session",
		Pattern: `^(TestAlpha|TestBeta)$`,
	}

	tests := []struct {
		name       string
		listOutput string
		wantError  string
	}{
		{
			name:       "zero matches",
			listOutput: "ok   example.test 0.001s\n",
			wantError:  "missing test name",
		},
		{
			name:       "partial matches",
			listOutput: "TestAlpha\nok   example.test 0.001s\n",
			wantError:  "missing test name",
		},
		{
			name:       "duplicate match",
			listOutput: "TestAlpha\nTestAlpha\nTestBeta\nok   example.test 0.001s\n",
			wantError:  "duplicate test name",
		},
		{
			name:       "unexpected match",
			listOutput: "TestAlpha\nTestBeta\nTestUnexpected\nok   example.test 0.001s\n",
			wantError:  "unexpected test name",
		},
		{
			name:       "malformed output",
			listOutput: "TestAlpha\nTestBeta\n",
			wantError:  "status line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeGoCommand{listOutput: tt.listOutput}
			err := runManifest([]windowsnative.Package{pkg}, nil, io.Discard, io.Discard, fake.run)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error=%v want substring %q", err, tt.wantError)
			}
			if len(fake.calls) != 1 || fake.calls[0][2] != "-list" {
				t.Fatalf("commands=%q want only list command", fake.calls)
			}
		})
	}
}

func TestRunManifestRejectsListCommandFailureAndSkipsExecution(t *testing.T) {
	pkg := windowsnative.Package{
		Path:    "./internal/session",
		Pattern: `^(TestAlpha)$`,
	}
	fake := &fakeGoCommand{
		listOutput: "TestAlpha\nok   example.test 0.001s\n",
		listErr:    errors.New("list failed"),
	}

	err := runManifest([]windowsnative.Package{pkg}, nil, io.Discard, io.Discard, fake.run)
	if err == nil || !strings.Contains(err.Error(), "list command failed") {
		t.Fatalf("error=%v want list command failure", err)
	}
	if len(fake.calls) != 1 || fake.calls[0][2] != "-list" {
		t.Fatalf("commands=%q want only list command", fake.calls)
	}
}

func TestRunManifestBoundsAndLabelsListFailureDiagnostics(t *testing.T) {
	pkg := windowsnative.Package{
		Path:    "./internal/session",
		Pattern: `^(TestAlpha)$`,
	}
	fake := &fakeGoCommand{
		listOutput:      "stdout-head\n" + strings.Repeat("s", 8192) + "\nstdout-tail",
		listErrorOutput: "stderr-head\n" + strings.Repeat("e", 8192) + "\nstderr-tail",
		listErr:         errors.New("exit status 1"),
	}

	err := runManifest([]windowsnative.Package{pkg}, nil, io.Discard, io.Discard, fake.run)
	if err == nil {
		t.Fatal("runManifest returned nil for failed list command")
	}
	message := err.Error()
	for _, want := range []string{
		"list stdout:",
		"list stderr:",
		"stdout-head",
		"stdout-tail",
		"stderr-head",
		"stderr-tail",
		"[...truncated...]",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error=%q missing %q", message, want)
		}
	}
	if len(message) > 2*4096+512 {
		t.Fatalf("diagnostic error length=%d is not bounded", len(message))
	}
	if len(fake.calls) != 1 || fake.calls[0][2] != "-list" {
		t.Fatalf("commands=%q want only list command", fake.calls)
	}
}
