package integration

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var currentListLabelPattern = regexp.MustCompile(`^[0-9]+/[0-9]+(?: \([1-9][0-9]*\))?$`)

type terminalScreen struct {
	cells       map[int]map[int]rune
	row, column int
	maxRow      int
	maxColumn   int
}

func currentListBorderLabel(output []byte) (string, bool) {
	screen := replayTerminalScreen(output)
	for row := 1; row <= screen.maxRow; row++ {
		line := screen.line(row)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "┌") && strings.HasSuffix(trimmed, "┐") {
			label := strings.Trim(innerBorder(trimmed, "┌", "┐"), "─ ")
			if currentListLabelPattern.MatchString(label) {
				return label, true
			}
		}
		if len(trimmed) >= 2 && strings.HasPrefix(trimmed, "│") && strings.HasSuffix(trimmed, "│") {
			label := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "│"), "│"))
			if currentListLabelPattern.MatchString(label) {
				return label, true
			}
		}
	}
	return "", false
}

func currentScreenContains(output []byte, text string) bool {
	screen := replayTerminalScreen(output)
	for row := 1; row <= screen.maxRow; row++ {
		if strings.Contains(screen.line(row), text) {
			return true
		}
	}
	return false
}

func waitForCurrentScreenTextAfter(t *testing.T, term terminalSession, before int, text string) {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if before < len(output) && currentScreenContains(output, text) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("current screen lacks %q after %d bytes: %v; output=%q", text, before, ctx.Err(), output)
		default:
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}

func innerBorder(line, left, right string) string {
	return strings.TrimSuffix(strings.TrimPrefix(line, left), right)
}

func replayTerminalScreen(output []byte) terminalScreen {
	screen := terminalScreen{cells: make(map[int]map[int]rune), row: 1, column: 1}
	for index := 0; index < len(output); {
		if output[index] == '\x1b' {
			index = screen.consumeEscape(output, index)
			continue
		}
		switch output[index] {
		case '\r':
			screen.column = 1
			index++
		case '\n':
			screen.row++
			screen.recordPosition()
			index++
		case '\b':
			if screen.column > 1 {
				screen.column--
			}
			index++
		case '\t':
			screen.column = ((screen.column-1)/8+1)*8 + 1
			screen.recordPosition()
			index++
		default:
			if output[index] < 0x20 || output[index] == 0x7f {
				index++
				continue
			}
			value, size := utf8.DecodeRune(output[index:])
			if value == utf8.RuneError && size == 1 {
				size = 1
			}
			screen.put(value)
			index += size
		}
	}
	return screen
}

func (screen *terminalScreen) consumeEscape(output []byte, index int) int {
	if index+1 >= len(output) {
		return len(output)
	}
	switch output[index+1] {
	case '[':
		end := index + 2
		for end < len(output) && (output[end] < 0x40 || output[end] > 0x7e) {
			end++
		}
		if end >= len(output) {
			return len(output)
		}
		screen.applyCSI(string(output[index+2:end]), output[end])
		return end + 1
	case ']':
		end := index + 2
		for end < len(output) {
			if output[end] == '\a' {
				return end + 1
			}
			if output[end] == '\x1b' && end+1 < len(output) && output[end+1] == '\\' {
				return end + 2
			}
			end++
		}
		return len(output)
	default:
		return index + 2
	}
}

func (screen *terminalScreen) applyCSI(parameters string, command byte) {
	private := strings.TrimLeft(parameters, "?>")
	values := strings.Split(private, ";")
	parameter := func(index, fallback int) int {
		if index >= len(values) || values[index] == "" {
			return fallback
		}
		value := 0
		for _, digit := range values[index] {
			if digit < '0' || digit > '9' {
				return fallback
			}
			value = value*10 + int(digit-'0')
		}
		return value
	}
	switch command {
	case 'H', 'f':
		screen.row, screen.column = parameter(0, 1), parameter(1, 1)
		screen.recordPosition()
	case 'G', '`':
		screen.column = parameter(0, 1)
		screen.recordPosition()
	case 'A':
		screen.row = max(1, screen.row-parameter(0, 1))
	case 'B', 'e':
		screen.row += parameter(0, 1)
		screen.recordPosition()
	case 'C', 'a':
		screen.column += parameter(0, 1)
		screen.recordPosition()
	case 'D':
		screen.column = max(1, screen.column-parameter(0, 1))
	case 'J':
		if parameter(0, 0) == 2 || parameter(0, 0) == 3 {
			screen.cells = make(map[int]map[int]rune)
			screen.maxRow, screen.maxColumn = 0, 0
		}
	case 'K':
		line := screen.cells[screen.row]
		if parameter(0, 0) == 2 {
			delete(screen.cells, screen.row)
			return
		}
		if parameter(0, 0) == 1 {
			for column := range line {
				if column <= screen.column {
					delete(line, column)
				}
			}
			return
		}
		for column := range line {
			if column >= screen.column {
				delete(line, column)
			}
		}
	case 'X':
		line := screen.cells[screen.row]
		for column := screen.column; column < screen.column+parameter(0, 1); column++ {
			delete(line, column)
		}
	}
}

func (screen *terminalScreen) put(value rune) {
	if screen.cells[screen.row] == nil {
		screen.cells[screen.row] = make(map[int]rune)
	}
	screen.cells[screen.row][screen.column] = value
	screen.recordPosition()
	screen.column++
	screen.recordPosition()
}

func (screen *terminalScreen) recordPosition() {
	screen.maxRow = max(screen.maxRow, screen.row)
	screen.maxColumn = max(screen.maxColumn, screen.column)
}

func (screen terminalScreen) line(row int) string {
	line := screen.cells[row]
	if len(line) == 0 {
		return ""
	}
	values := make([]rune, screen.maxColumn)
	for index := range values {
		values[index] = ' '
	}
	for column, value := range line {
		if column > 0 && column <= len(values) {
			values[column-1] = value
		}
	}
	return strings.TrimRight(string(values), " ")
}

func TestCurrentListBorderLabelUsesTheLatestScreenState(t *testing.T) {
	oldFrame := "\x1b[2J\x1b[H┌──────────┐\r\n│   0/0    │\r\n└──────────┘"
	currentFrame := "\x1b[2J\x1b[H┌──────────┐\r\n│   5/5    │\r\n└──────────┘"
	if got, ok := currentListBorderLabel([]byte(oldFrame + currentFrame)); !ok || got != "5/5" {
		t.Fatalf("currentListBorderLabel() = %q, %t; want 5/5, true", got, ok)
	}
}

func TestCurrentListBorderLabelTracksInPlaceFZFUpdate(t *testing.T) {
	output := []byte("\x1b[2J\x1b[H┌────────────────────┐\r\n│       0/5          │\r\n└────────────────────┘\x1b[1;9H1/5 (1)")
	if got, ok := currentListBorderLabel(output); !ok || got != "1/5 (1)" {
		t.Fatalf("currentListBorderLabel() = %q, %t; want 1/5 (1), true", got, ok)
	}
}

func TestCurrentListBorderLabelRejectsHistoricalSubstringOutsideCurrentBorder(t *testing.T) {
	output := []byte("historical 5/5 (1) text\x1b[2J\x1b[H┌──────────┐\r\n│          │\r\n└──────────┘")
	if got, ok := currentListBorderLabel(output); ok {
		t.Fatalf("currentListBorderLabel() = %q, %t; want no active label", got, ok)
	}
}

func TestCurrentScreenTextUsesTheLatestRenderedFrame(t *testing.T) {
	output := []byte("old-header\x1b[2J\x1b[Hcurrent-header")
	if !currentScreenContains(output, "current-header") {
		t.Fatal("currentScreenContains did not find the active header")
	}
	if currentScreenContains(output, "old-header") {
		t.Fatal("currentScreenContains found historical text outside the active frame")
	}
}
