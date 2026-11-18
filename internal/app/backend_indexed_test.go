package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestCurrentHeaderKeepsUnearthIdentityAcrossResize(t *testing.T) {
	actor := session.New(context.Background(), func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{}, nil
	})
	t.Cleanup(func() { _ = actor.Close() })
	_, err := actor.Apply(context.Background(), session.ProposedTransition{
		State: session.State{Picker: protocol.PickerCP, Source: protocol.SourceIndexed,
			Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/work")),
			Home: pathutil.Filesystem([]byte("/home"))},
		Build: &candidate.BuildRequest{Picker: protocol.PickerCP, Source: protocol.SourceIndexed},
	})
	if err != nil {
		t.Fatal(err)
	}
	indexed := &indexedSource{database: &fsearchdb.Database{SavedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}}
	backend := &pickerBackend{actor: actor, indexed: indexed, commandHeader: "cp ~/gits"}
	header, err := backend.CurrentHeader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(header, "cp ~/gits · Unearth") || !strings.Contains(header, "2024-01-02T03:04:05Z") {
		t.Fatalf("indexed header = %q", header)
	}
}

func TestCommandHeaderPreservesSourceSwitchStatus(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		t.Run(string(picker), func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			indexed := &indexedSource{database: &fsearchdb.Database{
				SavedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
			}}
			failIndex := false
			actor := session.New(ctx, func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
				if failIndex && request.Source == protocol.SourceIndexed {
					return candidate.BuildResult{}, errors.New("unavailable")
				}
				return candidate.BuildResult{}, nil
			})
			defer actor.Close()
			initial, err := actor.Apply(ctx, session.ProposedTransition{
				State: session.State{Picker: picker, Mode: protocol.ModeInsert,
					Location: pathutil.Filesystem([]byte(base)), Home: pathutil.Filesystem([]byte(base)),
					Completion: &session.CompletionContext{Base: base}},
				Build: &candidate.BuildRequest{Picker: picker},
			})
			if err != nil {
				t.Fatal(err)
			}
			command := string(picker) + " ~/gits"
			backend := &pickerBackend{actor: actor, indexed: indexed, commandHeader: command, metrics: &pickerMetrics{}}
			if picker == protocol.PickerCD {
				backend.enrichment = newTestEnrichment(t, ctx, actor,
					initialZoxideSourceFunc(func(context.Context) (candidate.InitialZoxideResult, error) {
						return candidate.InitialZoxideResult{}, nil
					}), fzf.NewInputStream(nil))
				if err := backend.enrichment.Activate(initial.Snapshot.Generation()); err != nil {
					t.Fatal(err)
				}
			}
			searchHeader := command + " · Unearth"
			if picker == protocol.PickerCD {
				searchHeader += " · directories only"
			}
			searchHeader += " · saved 2024-01-02T03:04:05Z"
			for _, want := range []string{searchHeader, command} {
				result, err := backend.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpSourceToggle})
				if err != nil || result.Header != want {
					t.Fatalf("source-switch header=%q, want %q; err=%v", result.Header, want, err)
				}
				if backend.enrichment != nil {
					acknowledgeEnrichmentEvent(t, backend.enrichment, result)
				}
				header, err := backend.CurrentHeader(ctx)
				if err != nil || header != want {
					t.Fatalf("refreshed header=%q, want %q; err=%v", header, want, err)
				}
			}
			failIndex = true
			result, err := backend.HandleEvent(ctx, protocol.Event{Opcode: protocol.OpSourceToggle})
			want := command + " · Unearth unavailable — Ctrl+F to retry"
			if err != nil || result.Header != want {
				t.Fatalf("failed-search header=%q, want %q; err=%v", result.Header, want, err)
			}
			if backend.enrichment != nil {
				acknowledgeEnrichmentEvent(t, backend.enrichment, result)
			}
		})
	}
}
