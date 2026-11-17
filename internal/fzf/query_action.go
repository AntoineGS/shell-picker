package fzf

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Keep this aligned with the saved-index reader's maximum entry count.
const maxRestorableCandidatePosition = 20_000_000

func changeQuery(query string) (action, error) {
	if query == "" {
		// The colon form needs a nonempty argument. Otherwise fzf absorbs
		// this action into a preceding colon-delimited header.
		return action{text: "change-query()"}, nil
	}
	if err := validateActionArgument(query); err != nil {
		return action{}, err
	}
	return action{text: "change-query:" + query}, nil
}

func restoreSelectionActions(encoded string) ([]action, error) {
	if encoded == "" {
		return nil, nil
	}
	parts := strings.Split(encoded, ",")
	if len(parts) > 1_000_000 {
		return nil, errors.New("fzf: too many selections to restore")
	}
	actions := make([]action, 0, len(parts)*2+2)
	actions = append(actions, action{text: "change-query()"}, wait())
	for _, part := range parts {
		position, err := strconv.Atoi(part)
		if err != nil || position < 1 || position > maxRestorableCandidatePosition || strconv.Itoa(position) != part {
			return nil, errors.New("fzf: invalid selection restore position")
		}
		actions = append(actions, action{text: fmt.Sprintf("pos(%d)", position)}, action{text: "select"})
	}
	return actions, nil
}
