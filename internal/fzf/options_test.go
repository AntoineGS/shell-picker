package fzf

import (
	"slices"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestPickerOptions(t *testing.T) {
	required := []string{
		"--keep-right",
		"--jump-labels=g",
		"--bind=ctrl-u:half-page-up",
		"--bind=ctrl-d:half-page-down",
		"--bind=g:jump",
		"--bind=G:last",
		"--bind=jump:first",
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
				strings.Contains(option, `,change,result-final)+unbind[(,),\]+transform(d)`)
		}) {
			t.Errorf("picker %q start binding does not disable restore events", picker)
		}
	}
}

func TestNormalModeIgnoresEveryOtherASCIIPrintableKey(t *testing.T) {
	active := map[rune]bool{
		'h': true, 'j': true, 'k': true, 'l': true, 'i': true, 'a': true, 'q': true,
		'g': true, 'G': true,
		'/': true, '~': true, ',': true, '.': true,
	}
	options := Options(protocol.PickerCD, "[I] ", "/work/")
	for key := '!'; key <= '~'; key++ {
		if active[key] {
			continue
		}
		want := binding(string(key), ignore())
		if !slices.Contains(options, want) {
			t.Errorf("printable key %q lacks exact ignore binding %q", key, want)
		}
	}
}

func TestInsertAndAddUnbindFullNormalOnlySet(t *testing.T) {
	ignored := make([]string, 0, 83)
	active := map[rune]bool{
		'h': true, 'j': true, 'k': true, 'l': true, 'i': true, 'a': true, 'q': true,
		'g': true, 'G': true,
		'/': true, '~': true, ',': true, '.': true,
	}
	for key := '!'; key <= '~'; key++ {
		if !active[key] {
			ignored = append(ignored, string(key))
		}
	}
	listPaging := []string{"ctrl-u", "ctrl-d"}
	normalOnly := append([]string{"h", "j", "k", "l", "i", "a", "q", "space", "g", "G", "jump", ",", "."}, ignored...)
	ordinaryNormalOnly := make([]string, 0, len(normalOnly)-3)
	for _, key := range normalOnly {
		if key == `\` || key == "(" || key == ")" {
			continue
		}
		key = strings.ReplaceAll(key, `\`, `\\`)
		ordinaryNormalOnly = append(ordinaryNormalOnly, strings.ReplaceAll(key, ",", `\,`))
	}
	wantNormalAction := "unbind(" + strings.Join(ordinaryNormalOnly, ",") + `)+unbind[(,),\]`

	insert, err := RenderEffect(protocol.Effect{Rebind: protocol.ModeInsert})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(insert, wantNormalAction) {
		t.Fatalf("insert=%q lacks %q", insert, wantNormalAction)
	}
	normal, err := RenderEffect(protocol.Effect{Rebind: protocol.ModeNormal})
	if err != nil {
		t.Fatal(err)
	}
	ordinaryAll := append([]string{"ctrl-l", "tab", "right", "ctrl-h", "left", "/", "~"}, listPaging...)
	ordinaryAll = append(ordinaryAll, ordinaryNormalOnly...)
	if want := "rebind(" + strings.Join(ordinaryAll, ",") + `)+rebind[(,),\]`; normal != want {
		t.Fatalf("normal=%q want=%q", normal, want)
	}

	add, err := RenderEffect(protocol.Effect{Rebind: protocol.ModeAdd})
	if err != nil {
		t.Fatal(err)
	}
	if want := "unbind(" + strings.Join(ordinaryAll, ",") + `)+unbind[(,),\]`; add != want {
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
