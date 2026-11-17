package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func TestCurrentHeaderKeepsFSearchIdentityAcrossResize(t *testing.T) {
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
	backend := &pickerBackend{actor: actor, indexed: indexed}
	header, err := backend.CurrentHeader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header, "FSearch") || !strings.Contains(header, "2024-01-02T03:04:05Z") {
		t.Fatalf("indexed header = %q", header)
	}
}
