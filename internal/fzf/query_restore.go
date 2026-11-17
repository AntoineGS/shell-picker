package fzf

import (
	"errors"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func changeHeader(header string) (action, error) {
	if header == "" {
		return action{}, errors.New("fzf: empty header")
	}
	if err := validateActionArgument(header); err != nil {
		return action{}, err
	}
	return action{text: "change-header:" + header}, nil
}

func effectHeaderAction(effect protocol.Effect) (action, error) {
	header, err := changeHeader(effect.Header)
	if err != nil {
		return action{}, err
	}
	if effect.SetQuery || effect.RestorePositions != "" || effect.SetSearchQuery {
		// A colon-delimited header consumes the rest of its action list.
		// Render it separately so restoration remains executable and
		// arbitrary path punctuation stays data, not action syntax.
		return transformDisplay(), nil
	}
	return header, nil
}

func queryRestoreActions(effect protocol.Effect) ([]action, error) {
	restoredSelections, err := restoreSelectionActions(effect.RestorePositions)
	if err != nil {
		return nil, err
	}
	actions := append([]action(nil), restoredSelections...)
	if effect.SetQuery {
		if effect.Completion {
			return append(actions, literalTransform("query", effect.Query)), nil
		}
		queryAction, err := changeQuery(effect.Query)
		if err != nil {
			return nil, err
		}
		actions = append(actions, queryAction)
	}
	return actions, nil
}
