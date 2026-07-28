package session

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type State struct {
	Picker   protocol.Picker
	Mode     protocol.Mode
	Location pathutil.Location
	Home     pathutil.Location
	Prompt   string
	AddError bool
}

type GenerateFunc func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error)

type cleanupFunc func(*pathutil.CreatedTree) error

type Snapshot struct {
	generation   uint64
	state        State
	records      []candidate.Record
	byFullRecord map[string][]int
}

type ProposedTransition struct {
	BaseGeneration uint64
	State          State
	Build          *candidate.BuildRequest
	Effect         protocol.Effect
	Created        *pathutil.CreatedTree
}

type TransitionMetrics struct {
	QueueWait         time.Duration
	TransformDuration time.Duration
	Sources           candidate.SourceMetrics
}

type TransitionResult struct {
	Snapshot Snapshot
	Effect   protocol.Effect
	Metrics  TransitionMetrics
}

type applyCommand struct {
	ctx       context.Context
	proposal  ProposedTransition
	submitted time.Time
	reply     chan applyReply
}

type applyReply struct {
	result TransitionResult
	err    error
}

type currentCommand struct {
	ctx   context.Context
	reply chan snapshotReply
}

type snapshotCommand struct {
	ctx        context.Context
	generation uint64
	reply      chan snapshotReply
}

type snapshotReply struct {
	snapshot Snapshot
	err      error
}

type resolveCommand struct {
	ctx   context.Context
	key   string
	reply chan resolveReply
}

type resolveReply struct {
	record candidate.Record
	err    error
}

type completionCommand struct {
	id     uint64
	result candidate.BuildResult
	err    error
}

type closeCommand struct {
	reply chan error
}

type pendingTransition struct {
	id       uint64
	command  *applyCommand
	accepted time.Time
	cancel   context.CancelCauseFunc
	retiring bool
	replyErr error
}

func (snapshot Snapshot) Generation() uint64 {
	return snapshot.generation
}

func (snapshot Snapshot) State() State {
	return cloneState(snapshot.state)
}

func (snapshot Snapshot) Records() []candidate.Record {
	return cloneRecords(snapshot.records)
}

func cloneState(state State) State {
	state.Location = cloneLocation(state.Location)
	state.Home = cloneLocation(state.Home)
	return state
}

func cloneLocation(location pathutil.Location) pathutil.Location {
	location.Path = bytes.Clone(location.Path)
	return location
}

func cloneRecord(record candidate.Record) candidate.Record {
	record.Path = bytes.Clone(record.Path)
	record.Target = cloneLocation(record.Target)
	return record
}

func cloneRecords(records []candidate.Record) []candidate.Record {
	if records == nil {
		return nil
	}
	cloned := make([]candidate.Record, len(records))
	for i, record := range records {
		cloned[i] = cloneRecord(record)
	}
	return cloned
}

func cloneProposal(proposal ProposedTransition) ProposedTransition {
	proposal.State = cloneState(proposal.State)
	if proposal.Build != nil {
		build := *proposal.Build
		build.Location = cloneLocation(build.Location)
		proposal.Build = &build
	}
	if proposal.Created != nil {
		created := *proposal.Created
		created.Target = cloneLocation(created.Target)
		created.Created = make([][]byte, len(proposal.Created.Created))
		for i, path := range proposal.Created.Created {
			created.Created[i] = bytes.Clone(path)
		}
		proposal.Created = &created
	}
	return proposal
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	return Snapshot{
		generation:   snapshot.generation,
		state:        cloneState(snapshot.state),
		records:      cloneRecords(snapshot.records),
		byFullRecord: cloneIndex(snapshot.byFullRecord),
	}
}

func cloneIndex(index map[string][]int) map[string][]int {
	if index == nil {
		return nil
	}
	cloned := make(map[string][]int, len(index))
	for key, positions := range index {
		cloned[key] = append([]int(nil), positions...)
	}
	return cloned
}

func transitionResult(snapshot Snapshot, command *applyCommand, accepted time.Time, effect protocol.Effect, sources candidate.SourceMetrics) TransitionResult {
	return TransitionResult{
		Snapshot: cloneSnapshot(snapshot), Effect: effect,
		Metrics: TransitionMetrics{QueueWait: accepted.Sub(command.submitted), TransformDuration: time.Since(command.submitted), Sources: sources},
	}
}

func buildIndex(records []candidate.Record) map[string][]int {
	index := make(map[string][]int, len(records))
	for position, record := range records {
		key := record.FullKey()
		index[key] = append(index[key], position)
	}
	return index
}

func rollback(created *pathutil.CreatedTree) error {
	if created == nil {
		return nil
	}
	if err := created.Rollback(); err != nil {
		return fmt.Errorf("rollback proposed transition: %w", err)
	}
	return nil
}
