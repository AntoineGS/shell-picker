package finderinfo

import (
	"errors"
	"fmt"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// MaxCount is the largest count accepted by Format.
const MaxCount = 1_000_000_000

// ErrInvalidCount reports a count outside the supported range.
var ErrInvalidCount = errors.New("finderinfo: invalid count")

// ValidateCounts validates the picker and each count independently. Relational
// validation belongs to consumers that own a typed state snapshot.
func ValidateCounts(picker protocol.Picker, matched, total, selected int) error {
	switch picker {
	case protocol.PickerCD, protocol.PickerCP:
	default:
		return fmt.Errorf("finderinfo: invalid picker %q", picker)
	}
	if !validCount(matched) || !validCount(total) || !validCount(selected) {
		return ErrInvalidCount
	}
	return nil
}

// Format renders finder counts using the display rules for picker.
func Format(picker protocol.Picker, matched, total, selected int) (string, error) {
	if err := ValidateCounts(picker, matched, total, selected); err != nil {
		return "", err
	}

	base := fmt.Sprintf("%d/%d", matched, total)
	if picker == protocol.PickerCP && selected > 0 {
		return fmt.Sprintf("%s (%d)", base, selected), nil
	}
	return base, nil
}

func validCount(value int) bool {
	return value >= 0 && value <= MaxCount
}
