package callback

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const (
	maxDisplayDimension = 1000
	maxFinderCount      = 1_000_000_000
)

func visibleHeader(header string, lookupEnv func(string) string) (string, bool) {
	if header == "" || !utf8.ValidString(header) {
		return "", false
	}
	columns, ok := parseCanonicalDecimal(lookupEnv("FZF_COLUMNS"), maxDisplayDimension, 4, false)
	if !ok {
		return "", false
	}
	contentWidth := columns - 4
	previewRaw := lookupEnv("FZF_PREVIEW_COLUMNS")
	if previewRaw != "" {
		preview, valid := parseCanonicalDecimal(previewRaw, maxDisplayDimension, 4, true)
		if !valid {
			return "", false
		}
		if preview > 0 {
			if preview*2 <= columns {
				contentWidth = columns - preview - 8
			} else {
				contentWidth = columns - 4
			}
		}
	}
	if contentWidth <= 0 {
		return "", false
	}
	if stringCellWidth(header) <= contentWidth {
		return header, true
	}

	const marker = "··"
	markerWidth := stringCellWidth(marker)
	if contentWidth > markerWidth {
		if tail := trailingCells(header, contentWidth-markerWidth); tail != "" {
			return marker + tail, true
		}
	}
	tail := trailingCells(header, contentWidth)
	return tail, tail != ""
}

func finderInfo(picker protocol.Picker, lookupEnv func(string) string) string {
	matched, matchedOK := parseCanonicalDecimal(lookupEnv("FZF_MATCH_COUNT"), maxFinderCount, 10, true)
	total, totalOK := parseCanonicalDecimal(lookupEnv("FZF_TOTAL_COUNT"), maxFinderCount, 10, true)
	selected, selectedOK := parseCanonicalDecimal(lookupEnv("FZF_SELECT_COUNT"), maxFinderCount, 10, true)
	if !matchedOK || !totalOK || !selectedOK {
		return ""
	}
	base := fmt.Sprintf("%d/%d", matched, total)
	switch picker {
	case protocol.PickerCD:
		return base
	case protocol.PickerCP:
		if selected > 0 {
			return fmt.Sprintf("%s (%d)", base, selected)
		}
		return base
	default:
		return ""
	}
}

func parseCanonicalDecimal(raw string, maximum, maxDigits int, allowZero bool) (int, bool) {
	if raw == "" || len(raw) > maxDigits || len(raw) > 1 && raw[0] == '0' {
		return 0, false
	}
	for _, digit := range []byte(raw) {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value > maximum || value == 0 && !allowZero {
		return 0, false
	}
	return value, true
}

func displayCellWidth(r rune) int {
	if r >= 0x20 && r <= 0x7e {
		return 1
	}
	return 2
}

func stringCellWidth(value string) int {
	width := 0
	for _, r := range value {
		width += displayCellWidth(r)
	}
	return width
}

func trailingCells(value string, maximum int) string {
	start := len(value)
	width := 0
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(value[:start])
		cellWidth := displayCellWidth(r)
		if width+cellWidth > maximum {
			break
		}
		start -= size
		width += cellWidth
	}
	return value[start:]
}
