package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestOrdinaryCPStartupDoesNotLoadFSearchDatabase(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	loads := 0
	fixture.dependencies.IndexLoader = func(context.Context, string) (*fsearchdb.Database, error) {
		loads++
		return nil, errors.New("unexpected index load")
	}
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		if _, err := io.ReadAll(config.Input); err != nil {
			return fzf.Result{}, err
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	if loads != 0 {
		t.Fatalf("index loads = %d, want 0 for ordinary cp startup", loads)
	}
}

func TestStandaloneSearchLoadsIndexAndReturnsAbsolutePaths(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerSearch)
	first := filepath.Join(fixture.cwd, "readme.md")
	second := filepath.Join(fixture.cwd, "child")
	loads := 0
	fixture.dependencies.IndexLoader = func(_ context.Context, path string) (*fsearchdb.Database, error) {
		loads++
		if path != filepath.Join(fixture.cwd, ".local", "share", "fsearch", "fsearch.db") {
			t.Fatalf("database path = %q", path)
		}
		return &fsearchdb.Database{Entries: []fsearchdb.Entry{
			{Path: []byte(first)}, {Path: []byte(second), IsDir: true},
		}}, nil
	}
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		foundHeader := false
		for _, option := range config.Options {
			if strings.HasPrefix(option, "--header=") && strings.Contains(option, "FSearch") {
				foundHeader = true
			}
		}
		if !foundHeader {
			t.Fatalf("search header does not identify FSearch: %q", config.Options)
		}
		framed, err := io.ReadAll(config.Input)
		if err != nil {
			return fzf.Result{}, err
		}
		records := bytes.Split(bytes.TrimSuffix(framed, []byte{0}), []byte{0})
		if len(records) != 2 {
			t.Fatalf("indexed candidates = %q", framed)
		}
		return fzf.Result{Key: "enter", Records: records}, nil
	}
	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if loads != 1 || outcome.Status != protocol.StatusAccepted || len(outcome.Paths) != 2 ||
		string(outcome.Paths[0]) != first || string(outcome.Paths[1]) != second {
		t.Fatalf("loads=%d outcome=%+v", loads, outcome)
	}
}

func TestStandaloneSearchReportsLoadFailureBeforeLaunchingFZF(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerSearch)
	launches := 0
	fixture.dependencies.IndexLoader = func(context.Context, string) (*fsearchdb.Database, error) {
		return nil, errors.New("database is busy")
	}
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		launches++
		return fzf.Result{}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err == nil ||
		!strings.Contains(err.Error(), "database is busy") {
		t.Fatalf("RunPicker error = %v, want useful load reason", err)
	}
	if launches != 0 {
		t.Fatalf("fzf launches = %d, want 0", launches)
	}
}
