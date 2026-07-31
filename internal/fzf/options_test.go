package fzf

import (
	"slices"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestPickerOptions(t *testing.T) {
	required := []string{
		"--bind=ctrl-u:half-page-up",
		"--bind=ctrl-d:half-page-down",
		"--bind=,:preview-half-page-up",
		"--bind=.:preview-half-page-down",
		"--bind=change:transform(e:rs)",
		"--bind=result-final:rebind(change)+unbind(result-final)",
	}
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		got := Options(picker, "[I] ", "/work/")
		for _, option := range required {
			if !slices.Contains(got, option) {
				t.Errorf("picker %q lacks option %q", picker, option)
			}
		}
		if !slices.Contains(got, "--prompt=[I] ") || !slices.Contains(got, "--header=/work/") {
			t.Errorf("picker %q lacks prompt/header options", picker)
		}
		if !slices.ContainsFunc(got, func(option string) bool {
			return strings.HasPrefix(option, "--bind=start:unbind(") &&
				strings.Contains(option, ",change,result-final)+transform(d)")
		}) {
			t.Errorf("picker %q start binding does not disable restore events", picker)
		}
	}
}

func TestNormalModeIgnoresEveryOtherASCIIPrintableKey(t *testing.T) {
	active := map[rune]bool{
		'h': true, 'j': true, 'k': true, 'l': true, 'i': true, 'a': true, 'q': true,
		'/': true, '~': true, ',': true, '.': true,
	}
	options := Options(protocol.PickerCD, "[I] ", "/work/")
	for key := '!'; key <= '~'; key++ {
		if active[key] {
			continue
		}
		encoded := strings.ReplaceAll(string(key), `\`, `\\`)
		encoded = strings.ReplaceAll(encoded, ",", `\,`)
		want := "--bind=" + encoded + ":ignore"
		if !slices.Contains(options, want) {
			t.Errorf("printable key %q lacks exact ignore binding %q", key, want)
		}
	}
}

func TestInsertAndAddUnbindFullNormalOnlySet(t *testing.T) {
	ignored := make([]string, 0, 83)
	active := map[rune]bool{
		'h': true, 'j': true, 'k': true, 'l': true, 'i': true, 'a': true, 'q': true,
		'/': true, '~': true, ',': true, '.': true,
	}
	for key := '!'; key <= '~'; key++ {
		if !active[key] {
			ignored = append(ignored, string(key))
		}
	}
	normalOnly := append([]string{"h", "j", "k", "l", "i", "a", "q", "space", "ctrl-u", "ctrl-d", ",", "."}, ignored...)
	encoded := make([]string, len(normalOnly))
	for index, key := range normalOnly {
		key = strings.ReplaceAll(key, `\`, `\\`)
		encoded[index] = strings.ReplaceAll(key, ",", `\,`)
	}

	insert, err := RenderEffect(protocol.Effect{Rebind: protocol.ModeInsert})
	if err != nil {
		t.Fatal(err)
	}
	if want := "unbind(" + strings.Join(encoded, ",") + ")"; !strings.Contains(insert, want) {
		t.Fatalf("insert=%q lacks %q", insert, want)
	}
	normal, err := RenderEffect(protocol.Effect{Rebind: protocol.ModeNormal})
	if err != nil {
		t.Fatal(err)
	}
	all := append([]string{"ctrl-l", "tab", "right", "ctrl-h", "left", "/", "~"}, normalOnly...)
	allEncoded := make([]string, 0, len(all))
	for _, key := range all {
		key = strings.ReplaceAll(key, `\`, `\\`)
		allEncoded = append(allEncoded, strings.ReplaceAll(key, ",", `\,`))
	}
	if want := "rebind(" + strings.Join(allEncoded, ",") + ")"; normal != want {
		t.Fatalf("normal=%q want=%q", normal, want)
	}

	add, err := RenderEffect(protocol.Effect{Rebind: protocol.ModeAdd})
	if err != nil {
		t.Fatal(err)
	}
	encoded = encoded[:0]
	for _, key := range all {
		key = strings.ReplaceAll(key, `\`, `\\`)
		encoded = append(encoded, strings.ReplaceAll(key, ",", `\,`))
	}
	if want := "unbind(" + strings.Join(encoded, ",") + ")"; add != want {
		t.Fatalf("add=%q want=%q", add, want)
	}
}

func TestOptionsHaveNoListenOrDuplicateBindings(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		options := Options(picker, "prompt", "header")
		keys := map[string]struct{}{}
		for _, option := range options {
			if strings.HasPrefix(option, "--listen") {
				t.Fatalf("picker %q has listen option %q", picker, option)
			}
			if !strings.HasPrefix(option, "--bind=") {
				continue
			}
			binding := strings.TrimPrefix(option, "--bind=")
			key, _, ok := strings.Cut(binding, ":")
			if !ok {
				t.Fatalf("malformed binding %q", option)
			}
			if _, duplicate := keys[key]; duplicate {
				t.Fatalf("duplicate binding %q", key)
			}
			keys[key] = struct{}{}
		}
		if !slices.Contains(options, "--read0") || !slices.Contains(options, "--print0") {
			t.Fatalf("picker %q lacks NUL options", picker)
		}
	}
}
