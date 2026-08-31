package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	ErrInvalidEvent      = errors.New("invalid session event")
	ErrInvalidNavigation = errors.New("invalid navigation target")
)

func Handle(ctx context.Context, actor *Actor, event protocol.Event) (TransitionResult, error) {
	if ctx == nil {
		return TransitionResult{}, errNilContext
	}
	if actor == nil {
		return TransitionResult{}, errors.New("session handle: nil actor")
	}
	snapshot, err := actor.Current(ctx)
	if err != nil {
		return TransitionResult{}, err
	}
	reduction, err := Reduce(snapshot, event)
	if err != nil {
		return TransitionResult{}, err
	}
	if proposal, ok := reduction.proposalValue(); ok {
		return actor.Apply(ctx, proposal)
	}
	intent, ok := reduction.addIntentValue()
	if !ok {
		return TransitionResult{}, errors.New("session handle: invalid reduction")
	}
	created, createErr := pathutil.CreateDirectoryTree(intent.base, intent.query)
	if createErr != nil {
		return actor.Apply(ctx, addErrorProposal(snapshot))
	}
	proposal := ProposedTransition{BaseGeneration: intent.baseGeneration, State: cloneState(snapshot.state), Created: &created}
	setNavigationUnchecked(&proposal, created.Target)
	if cause := context.Cause(ctx); cause != nil {
		return TransitionResult{}, errors.Join(cause, rollback(proposal.Created))
	}
	return actor.Apply(ctx, proposal)
}

func Reduce(snapshot Snapshot, event protocol.Event) (Reduction, error) {
	proposal := ProposedTransition{
		BaseGeneration: snapshot.generation,
		State:          cloneState(snapshot.state),
	}
	state := snapshot.state
	switch event.Opcode {
	case protocol.OpModeInsert:
		if state.Mode != protocol.ModeNormal {
			return Reduction{}, fmt.Errorf("%w: insert mode is unbound in %s", ErrInvalidEvent, state.Mode)
		}
		setMode(&proposal, protocol.ModeInsert, false)
	case protocol.OpModeAdd:
		if state.Mode != protocol.ModeNormal {
			return Reduction{}, fmt.Errorf("%w: add mode is unbound in %s", ErrInvalidEvent, state.Mode)
		}
		setMode(&proposal, protocol.ModeAdd, true)
	case protocol.OpEscape:
		reduceEscape(&proposal)
	case protocol.OpForward:
		if state.Mode == protocol.ModeAdd {
			proposal.Effect.Ignore = true
			break
		}
		record, err := resolveNavigationRecord(snapshot, event.CurrentItem)
		if err != nil {
			return Reduction{}, err
		}
		if !canNavigate(state.Picker, record.Kind) {
			return Reduction{}, fmt.Errorf("%w: %s cannot navigate %s", ErrInvalidNavigation, state.Picker, record.Kind)
		}
		target, err := navigationTarget(record)
		if err != nil {
			return Reduction{}, err
		}
		setNavigationUnchecked(&proposal, target)
	case protocol.OpParent:
		if state.Mode == protocol.ModeAdd {
			proposal.Effect.Ignore = true
			break
		}
		setNavigationUnchecked(&proposal, pathutil.Parent(state.Location))
	case protocol.OpSlash:
		if state.Mode == protocol.ModeAdd {
			proposal.Effect.Put = "/"
			break
		}
		if len(event.Query) == 0 {
			setNavigationUnchecked(&proposal, pathutil.Root())
		} else if state.Mode == protocol.ModeInsert && bytes.Equal(event.Query, []byte("..")) {
			setNavigationUnchecked(&proposal, pathutil.Parent(state.Location))
		} else if state.Mode == protocol.ModeInsert {
			record, ok := exactImmediateChild(snapshot, event.Query)
			if !ok {
				proposal.Effect = protocol.Effect{Put: "/", InvalidPath: true}
				break
			}
			target, err := navigationTarget(record)
			if err != nil {
				return Reduction{}, err
			}
			setNavigationUnchecked(&proposal, target)
		} else {
			proposal.Effect.Ignore = true
		}
	case protocol.OpHome:
		if state.Mode == protocol.ModeAdd || (state.Mode == protocol.ModeInsert && len(event.Query) != 0) {
			proposal.Effect.Put = "~"
		} else if state.Mode == protocol.ModeNormal && len(event.Query) != 0 {
			proposal.Effect.Ignore = true
		} else {
			setNavigationUnchecked(&proposal, state.Home)
		}
	case protocol.OpEnter:
		return reduceEnter(snapshot, proposal, event)
	case protocol.OpRestoreView:
		proposal.Effect = protocol.Effect{RestoreGeneration: snapshot.generation}
	default:
		return Reduction{}, fmt.Errorf("%w: unknown opcode %q", ErrInvalidEvent, event.Opcode)
	}
	return proposalReduction(proposal), nil
}

func reduceEscape(proposal *ProposedTransition) {
	switch proposal.State.Mode {
	case protocol.ModeInsert:
		setMode(proposal, protocol.ModeNormal, false)
	case protocol.ModeNormal:
		proposal.Effect = protocol.Effect{Abort: true}
	case protocol.ModeAdd:
		setMode(proposal, protocol.ModeNormal, true)
	}
}

func reduceEnter(snapshot Snapshot, proposal ProposedTransition, event protocol.Event) (Reduction, error) {
	if snapshot.state.Mode == protocol.ModeAdd {
		if err := pathutil.ValidateAddQuery(snapshot.state.Location, event.Query); err != nil {
			return proposalReduction(addErrorProposal(snapshot)), nil
		}
		return addReduction(snapshot.generation, snapshot.state.Location, event.Query), nil
	}
	if len(event.CurrentItem) != 0 {
		record, err := resolveRecord(snapshot, event.CurrentItem)
		if err != nil {
			return Reduction{}, err
		}
		if record.Kind == protocol.KindVirtual {
			target, err := navigationTarget(record)
			if err != nil {
				return Reduction{}, err
			}
			setNavigationUnchecked(&proposal, target)
			return proposalReduction(proposal), nil
		}
	}
	proposal.Effect.Accept = true
	return proposalReduction(proposal), nil
}

func addErrorProposal(snapshot Snapshot) ProposedTransition {
	state := cloneState(snapshot.state)
	state.AddError = true
	state.Prompt = modePrompt(protocol.ModeAdd, true)
	return ProposedTransition{
		BaseGeneration: snapshot.generation,
		State:          state,
		Effect:         protocol.Effect{Prompt: state.Prompt, Header: addErrorHeader(state), ErrorPrompt: true},
	}
}

func setMode(proposal *ProposedTransition, mode protocol.Mode, clearQuery bool) {
	proposal.State.Mode = mode
	proposal.State.AddError = false
	proposal.State.Prompt = modePrompt(mode, false)
	proposal.Effect = modeEffect(proposal.State, clearQuery, proposal.BaseGeneration)
}

func modeEffect(state State, clearQuery bool, restoreGeneration uint64) protocol.Effect {
	effect := protocol.Effect{
		Mode: state.Mode, Prompt: state.Prompt, Search: "on", Rebind: state.Mode,
		ClearQuery: clearQuery, Cursor: protocol.CursorLine, RestoreGeneration: restoreGeneration,
	}
	if state.Mode == protocol.ModeNormal {
		effect.Search = "off"
		effect.Cursor = protocol.CursorBlock
	}
	return effect
}

func modePrompt(mode protocol.Mode, addError bool) string {
	switch mode {
	case protocol.ModeNormal:
		return "[N] "
	case protocol.ModeAdd:
		if addError {
			return "[A!] "
		}
		return "[A] "
	default:
		return "[I] "
	}
}

func exactImmediateChild(snapshot Snapshot, query []byte) (candidate.Record, bool) {
	state := snapshot.state
	if state.Location.Kind != pathutil.KindFilesystem {
		return candidate.Record{}, false
	}
	for _, record := range snapshot.recordValues() {
		if record.Kind != protocol.KindLocal && record.Kind != protocol.KindDirectory {
			continue
		}
		if record.Target.Kind != pathutil.KindFilesystem ||
			!bytes.Equal([]byte(filepath.Dir(string(record.Target.Path))), state.Location.Path) ||
			!bytes.EqualFold([]byte(filepath.Base(string(record.Target.Path))), query) {
			continue
		}
		return cloneRecord(record), true
	}
	return candidate.Record{}, false
}

func navigationTarget(record candidate.Record) (pathutil.Location, error) {
	if record.Kind == protocol.KindVirtual {
		if record.Target.Kind != pathutil.KindDrives || len(record.Target.Path) != 0 {
			return pathutil.Location{}, fmt.Errorf("%w: virtual record does not target Drives", ErrInvalidNavigation)
		}
		return cloneLocation(record.Target), nil
	}
	if record.Target.Kind != pathutil.KindFilesystem {
		return pathutil.Location{}, fmt.Errorf("%w: filesystem record has non-filesystem target", ErrInvalidNavigation)
	}
	return cloneLocation(record.Target), nil
}

func setNavigationUnchecked(proposal *ProposedTransition, target pathutil.Location) {
	proposal.State.Location = cloneLocation(target)
	if proposal.State.Mode == protocol.ModeAdd {
		proposal.State.Mode = protocol.ModeNormal
	}
	proposal.State.AddError = false
	proposal.State.Prompt = modePrompt(proposal.State.Mode, false)
	proposal.Build = &candidate.BuildRequest{Picker: proposal.State.Picker, Location: cloneLocation(target)}
	proposal.Effect = modeEffect(proposal.State, true, 0)
	proposal.Effect.Header = pathutil.PromptDisplayHome(target, proposal.State.Home)
	proposal.Effect.ClearMulti = true
}

func resolveNavigationRecord(snapshot Snapshot, raw []byte) (candidate.Record, error) {
	if len(raw) == 0 {
		return candidate.Record{}, fmt.Errorf("%w: empty current item", ErrInvalidNavigation)
	}
	record, err := resolveRecord(snapshot, raw)
	if err != nil {
		return candidate.Record{}, fmt.Errorf("%w: %v", ErrInvalidNavigation, err)
	}
	return record, nil
}

func resolveRecord(snapshot Snapshot, raw []byte) (candidate.Record, error) {
	_, err := protocol.ParseRecord(raw)
	if err != nil {
		return candidate.Record{}, fmt.Errorf("%w: malformed record: %v", ErrUnknownRecord, err)
	}
	record, ok := snapshot.lookupRecord(string(raw))
	if !ok {
		return candidate.Record{}, ErrUnknownRecord
	}
	return cloneRecord(record), nil
}

func canNavigate(picker protocol.Picker, kind protocol.Kind) bool {
	if picker == protocol.PickerCD {
		return kind == protocol.KindLocal || kind == protocol.KindDirectory || kind == protocol.KindZoxide ||
			kind == protocol.KindDrive || kind == protocol.KindVirtual
	}
	return picker == protocol.PickerCP && (kind == protocol.KindDirectory || kind == protocol.KindDrive || kind == protocol.KindVirtual)
}
