package callback

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestFinderInfoPickerSelectionRules(t *testing.T) {
	for _, test := range []struct {
		picker   protocol.Picker
		selected string
		want     string
	}{
		{protocol.PickerCD, "0", "7/42"},
		{protocol.PickerCD, "1", "7/42"},
		{protocol.PickerCP, "0", "7/42"},
		{protocol.PickerCP, "1", "7/42 (1)"},
	} {
		env := map[string]string{"FZF_MATCH_COUNT": "7", "FZF_TOTAL_COUNT": "42", "FZF_SELECT_COUNT": test.selected}
		if got := finderInfo(test.picker, func(key string) string { return env[key] }); got != test.want {
			t.Fatalf("picker=%s selected=%s got=%q want=%q", test.picker, test.selected, got, test.want)
		}
	}
}

func TestFinderInfoRejectsInvalidCounters(t *testing.T) {
	invalid := []string{"", "-1", "+1", "01", " 1", "1 ", "x", "1000000001", "10000000000", "00000000000"}
	for _, value := range invalid {
		for _, key := range []string{"FZF_MATCH_COUNT", "FZF_TOTAL_COUNT", "FZF_SELECT_COUNT"} {
			env := map[string]string{"FZF_MATCH_COUNT": "7", "FZF_TOTAL_COUNT": "42", "FZF_SELECT_COUNT": "1"}
			env[key] = value
			if got := finderInfo(protocol.PickerCP, func(name string) string { return env[name] }); got != "" {
				t.Fatalf("key=%s value=%q got=%q", key, value, got)
			}
		}
	}
}

func TestFinderInfoAcceptsMaximumCounters(t *testing.T) {
	env := map[string]string{
		"FZF_MATCH_COUNT": "1000000000", "FZF_TOTAL_COUNT": "1000000000", "FZF_SELECT_COUNT": "1000000000",
	}
	want := "1000000000/1000000000 (1000000000)"
	if got := finderInfo(protocol.PickerCP, func(key string) string { return env[key] }); got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestVisibleHeaderWidthAndTruncation(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		columns string
		preview string
		want    string
	}{
		{name: "exact fit", header: "abcdef", columns: "10", want: "abcdef"},
		{name: "marker and tail", header: "abcdefgh", columns: "10", want: "··gh"},
		{name: "width one", header: "abcdef", columns: "5", want: "f"},
		{name: "width two", header: "abcdef", columns: "6", want: "ef"},
		{name: "width three", header: "abcdef", columns: "7", want: "def"},
		{name: "width four", header: "abcdef", columns: "8", want: "cdef"},
		{name: "empty preview", header: "abcdefgh", columns: "10", preview: "", want: "··gh"},
		{name: "zero preview", header: "abcdefgh", columns: "10", preview: "0", want: "··gh"},
		{name: "right half preview", header: strings.Repeat("a", 37), columns: "80", preview: "36", want: "··" + strings.Repeat("a", 32)},
		{name: "stacked preview keeps full width", header: strings.Repeat("a", 70), columns: "80", preview: "76", want: strings.Repeat("a", 70)},
		{name: "stacked unicode tail", header: strings.Repeat("a", 80) + "界/end", columns: "80", preview: "76", want: "··" + strings.Repeat("a", 66) + "界/end"},
		{name: "unix root", header: "/", columns: "5", want: "/"},
		{name: "windows root", header: `C:\`, columns: "7", want: `C:\`},
		{name: "virtual root", header: "Drives/", columns: "11", want: "Drives/"},
		{name: "escaped byte", header: `prefix\xFF`, columns: "8", want: `\xFF`},
		{name: "unicode exact fit", header: "ab界cd", columns: "10", want: "ab界cd"},
		{name: "unicode tail", header: "abc界d", columns: "8", want: "c界d"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{"FZF_COLUMNS": test.columns, "FZF_PREVIEW_COLUMNS": test.preview}
			got, ok := visibleHeader(test.header, func(key string) string { return env[key] })
			if !ok || got != test.want || !utf8.ValidString(got) {
				t.Fatalf("got=%q ok=%v want=%q valid=%v", got, ok, test.want, utf8.ValidString(got))
			}
		})
	}
}

func TestVisibleHeaderRejectsInvalidInputAndDimensions(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		columns string
		preview string
	}{
		{name: "empty header", columns: "80"},
		{name: "invalid utf8", header: string([]byte{'a', 0xff}), columns: "80"},
		{name: "missing columns", header: "/work/"},
		{name: "zero columns", header: "/work/", columns: "0"},
		{name: "negative columns", header: "/work/", columns: "-1"},
		{name: "leading zero columns", header: "/work/", columns: "080"},
		{name: "large columns", header: "/work/", columns: "1001"},
		{name: "bad preview", header: "/work/", columns: "80", preview: "x"},
		{name: "negative preview", header: "/work/", columns: "80", preview: "-1"},
		{name: "leading zero preview", header: "/work/", columns: "80", preview: "01"},
		{name: "large preview", header: "/work/", columns: "80", preview: "1001"},
		{name: "nonpositive width without preview", header: "/work/", columns: "4"},
		{name: "nonpositive width with preview", header: "/work/", columns: "9", preview: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{"FZF_COLUMNS": test.columns, "FZF_PREVIEW_COLUMNS": test.preview}
			if got, ok := visibleHeader(test.header, func(key string) string { return env[key] }); ok || got != "" {
				t.Fatalf("got=%q ok=%v", got, ok)
			}
		})
	}
}
