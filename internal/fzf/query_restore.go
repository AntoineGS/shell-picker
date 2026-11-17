package fzf

import "github.com/AntoineGS/shell-picker/internal/protocol"

func queryRestoreActions(effect protocol.Effect) ([]action, error) {
	restoredSelections, err := restoreSelectionActions(effect.RestorePositions)
	if err != nil {
		return nil, err
	}
	actions := append([]action(nil), restoredSelections...)
	if effect.SetQuery {
		queryAction, err := changeQuery(effect.Query)
		if err != nil {
			return nil, err
		}
		actions = append(actions, queryAction)
	}
	return actions, nil
}
