package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func newTracedEnrichment(t *testing.T, parent context.Context, actor *session.Actor, source initialZoxideLoader, stream *fzf.InputStream, metrics *pickerMetrics) (*initialEnrichment, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	trace := &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1, 2, 3})}
	enrichment, err := newInitialEnrichment(parent, actor, source, stream, metrics, trace, candidate.ZoxideCached)
	if err != nil {
		t.Fatalf("newInitialEnrichment: %v", err)
	}
	t.Cleanup(func() {
		enrichment.Stop(nil)
		_ = enrichment.Wait()
	})
	return enrichment, &output
}

func tracedEnrichmentRecords(t *testing.T, output *bytes.Buffer) []integrationpkg.TraceRecord {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	records := make([]integrationpkg.TraceRecord, 0, len(lines))
	for _, line := range lines {
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode trace line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestInitialEnrichmentEmitsOneStandalonePublishedTerminal(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), result: enrichmentSource("/published"),
	}
	metrics := &pickerMetrics{sources: candidate.SourceMetrics{ZoxideOutcome: "not-run"}}
	stream := fzf.NewInputStream(nil)
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, stream, metrics)
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	records := tracedEnrichmentRecords(t, output)
	var terminals []integrationpkg.TraceRecord
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminals = append(terminals, record)
		}
		if record.Event == "generation.publish" {
			t.Fatalf("Actor.Enrich emitted normal generation terminal: %+v", record)
		}
	}
	if len(terminals) != 1 {
		t.Fatalf("zoxide terminals=%d records=%+v", len(terminals), records)
	}
	terminal := terminals[0]
	if terminal.Outcome != "published" || terminal.Generation != 2 || terminal.CandidateCount != 2 ||
		terminal.ZoxidePolicy != "cached" || terminal.ZoxideOutcome != "cached" || terminal.ZoxideAttempts != 1 {
		t.Fatalf("terminal=%+v", terminal)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.sources.ZoxideAttempts != 1 || metrics.sources.ZoxideDuration <= 0 || metrics.sources.ZoxideOutcome != "cached" {
		t.Fatalf("metrics=%+v", metrics.sources)
	}
}

func TestInitialEnrichmentEmitsDiscardedTerminalForAllDuplicateResult(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}),
		result: candidate.InitialZoxideResult{
			Records: []candidate.Record{enrichmentRecord(protocol.KindZoxide, "/base")},
			Metrics: candidate.SourceMetrics{ZoxideOutcome: "ok", ZoxideAttempts: 1},
		},
	}
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil), &pickerMetrics{})
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	records := tracedEnrichmentRecords(t, output)
	var terminals []integrationpkg.TraceRecord
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminals = append(terminals, record)
		}
	}
	if len(terminals) != 1 || terminals[0].Outcome != "discarded" || terminals[0].Generation != 1 || terminals[0].CandidateCount != 0 || terminals[0].ZoxideOutcome != "ok" {
		t.Fatalf("terminals=%+v records=%+v", terminals, records)
	}
}

func TestInitialEnrichmentEmitsFailedTerminalForEachSoftSourceOutcome(t *testing.T) {
	for _, outcome := range []string{"missing", "process-error", "malformed", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			actor := newEnrichmentActor(t, protocol.ModeInsert)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}),
				result: candidate.InitialZoxideResult{Discarded: true, Metrics: candidate.SourceMetrics{ZoxideOutcome: outcome, ZoxideAttempts: 1}},
			}
			stream := fzf.NewInputStream(nil)
			enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, stream, &pickerMetrics{})
			if err := enrichment.Activate(1); err != nil {
				t.Fatalf("Activate: %v", err)
			}
			if err := awaitEnrichmentWait(t, enrichment); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			records := tracedEnrichmentRecords(t, output)
			var terminal integrationpkg.TraceRecord
			count := 0
			for _, record := range records {
				if record.Event == "zoxide.enrichment" {
					terminal = record
					count++
				}
			}
			if count != 1 || terminal.Outcome != "failed" || terminal.Generation != 1 || terminal.CandidateCount != 0 || terminal.ZoxideOutcome != outcome {
				t.Fatalf("count=%d terminal=%+v records=%+v", count, terminal, records)
			}
		})
	}
}

func TestInitialEnrichmentNavigationAndInputCloseDiscardOneTerminal(t *testing.T) {
	tests := []struct {
		name       string
		inputClose bool
		navigate   bool
		terminal   bool
		mode       protocol.Mode
	}{
		{name: "navigation", navigate: true, mode: protocol.ModeInsert},
		{name: "input-close", inputClose: true, mode: protocol.ModeInsert},
		{name: "terminal", terminal: true, mode: protocol.ModeNormal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actor := newEnrichmentActor(t, test.mode)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
				result: enrichmentSource("/discarded"), ignoreCtx: true,
			}
			stream := fzf.NewInputStream(nil)
			enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, stream, &pickerMetrics{})
			awaitEnrichmentChannel(t, source.started, "zoxide source")
			if test.inputClose {
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if err := enrichment.Activate(1); err != nil {
					t.Fatalf("Activate: %v", err)
				}
			} else if test.terminal {
				if err := enrichment.Activate(1); err != nil {
					t.Fatalf("Activate: %v", err)
				}
				if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpEscape}); err != nil {
					t.Fatalf("terminal: %v", err)
				}
			} else {
				if err := enrichment.Activate(1); err != nil {
					t.Fatalf("Activate: %v", err)
				}
				if _, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent}); err != nil {
					t.Fatalf("navigation: %v", err)
				}
			}
			close(source.release)
			if err := awaitEnrichmentWait(t, enrichment); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			records := tracedEnrichmentRecords(t, output)
			count := 0
			for _, record := range records {
				if record.Event == "zoxide.enrichment" {
					if record.Outcome != "discarded" || record.CandidateCount != 0 || record.ZoxideOutcome != "cached" {
						t.Fatalf("discard terminal=%+v", record)
					}
					count++
				}
			}
			if count != 1 {
				t.Fatalf("terminal count=%d records=%+v", count, records)
			}
		})
	}
}

func TestInitialEnrichmentEmitsDiscardedTerminalForStaleBase(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/stale"), ignoreCtx: true,
	}
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil), &pickerMetrics{})
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if _, err := actor.Apply(context.Background(), session.ProposedTransition{
		BaseGeneration: 1,
		State:          session.State{Picker: protocol.PickerCD, Mode: protocol.ModeInsert, Location: pathutil.Filesystem([]byte("/next"))},
		Build:          &candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte("/next"))},
	}); err != nil {
		t.Fatalf("publish newer actor base: %v", err)
	}
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	records := tracedEnrichmentRecords(t, output)
	var terminal integrationpkg.TraceRecord
	count := 0
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminal = record
			count++
		}
	}
	if count != 1 || terminal.Outcome != "discarded" || terminal.Generation != 1 || terminal.CandidateCount != 0 || terminal.ZoxideOutcome != "cached" {
		t.Fatalf("count=%d terminal=%+v records=%+v", count, terminal, records)
	}
}

func TestInitialEnrichmentMultipleNavigationDiscardUsesInitialGeneration(t *testing.T) {
	actor := newEnrichmentActor(t, protocol.ModeInsert)
	source := &controlledInitialZoxideSource{
		started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
		result: enrichmentSource("/late"), ignoreCtx: true,
	}
	enrichment, output := newTracedEnrichment(t, context.Background(), actor, source, fzf.NewInputStream(nil), &pickerMetrics{})
	awaitEnrichmentChannel(t, source.started, "zoxide source")
	if err := enrichment.Activate(1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	for navigation := 0; navigation < 3; navigation++ {
		effect, err := enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpParent})
		if err != nil || effect.ReloadGeneration != uint64(navigation+2) {
			t.Fatalf("navigation %d effect=%+v err=%v", navigation+1, effect, err)
		}
		acknowledgeEnrichmentEvent(t, enrichment, effect)
	}
	close(source.release)
	if err := awaitEnrichmentWait(t, enrichment); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	records := tracedEnrichmentRecords(t, output)
	var terminal integrationpkg.TraceRecord
	count := 0
	for _, record := range records {
		if record.Event == "zoxide.enrichment" {
			terminal = record
			count++
		}
	}
	if count != 1 || terminal.Outcome != "discarded" || terminal.Generation != 1 || terminal.CandidateCount != 0 || terminal.ZoxideOutcome != "cached" {
		t.Fatalf("count=%d terminal=%+v records=%+v", count, terminal, records)
	}
}

func TestInitialEnrichmentParentCancellationAndHardSourceFailureEmitFailedTerminal(t *testing.T) {
	tests := []struct {
		name       string
		parent     bool
		sourceErr  error
		wantSource string
	}{
		{name: "parent-cancel", parent: true, wantSource: "cancelled"},
		{name: "hard-source", sourceErr: errors.New("source failed"), wantSource: "process-error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := context.Background()
			cancel := context.CancelCauseFunc(func(error) {})
			if test.parent {
				parent, cancel = context.WithCancelCause(parent)
			}
			defer cancel(nil)
			actor := newEnrichmentActor(t, protocol.ModeInsert)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
				result: candidate.InitialZoxideResult{Discarded: true, Metrics: candidate.SourceMetrics{
					ZoxideOutcome: test.wantSource, ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1,
					ZoxideProcesses: 1, ZoxideMaxLive: 1,
				}}, err: test.sourceErr,
				returnResultOnCancel: test.parent,
			}
			stream := fzf.NewInputStream(nil)
			enrichment, output := newTracedEnrichment(t, parent, actor, source, stream, &pickerMetrics{})
			awaitEnrichmentChannel(t, source.started, "zoxide source")
			if test.parent {
				cancel(errors.New("parent stopped"))
			}
			close(source.release)
			_ = enrichment.Activate(1)
			_ = awaitEnrichmentWait(t, enrichment)
			records := tracedEnrichmentRecords(t, output)
			count := 0
			for _, record := range records {
				if record.Event == "zoxide.enrichment" {
					if record.Outcome != "failed" || record.ZoxideOutcome != test.wantSource || record.Generation == 0 ||
						record.ZoxideAttempts != 1 || record.ZoxideStarts != 1 || record.ZoxideExits != 1 ||
						record.ZoxideProcesses != 1 || record.ZoxideLive != 0 || record.ZoxideMaxLive != 1 {
						t.Fatalf("failed terminal=%+v", record)
					}
					count++
				}
			}
			if count != 1 {
				t.Fatalf("terminal count=%d records=%+v", count, records)
			}
		})
	}
}
