package fzf

import (
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRenderNavigationEffect(t *testing.T) {
	got, err := RenderEffect(protocol.Effect{Mode: protocol.ModeNormal, Prompt: `[N] a\)b/ `, ClearMulti: true, ClearQuery: true, ReloadGeneration: 7})
	want := `clear-multi+reload-sync(l:7)+change-prompt([N] a\\\)b/ )+clear-query+wait+first`
	if err != nil || got != want {
		t.Fatalf("got=%q want=%q err=%v", got, want, err)
	}
}

func TestRenderModeEffects(t *testing.T) {
	tests := []struct {
		name   string
		effect protocol.Effect
		want   string
	}{
		{"insert", protocol.Effect{Search: "on", Rebind: protocol.ModeInsert, Prompt: "[I] /work/ "},
			"enable-search+rebind(ctrl-l,tab,right,ctrl-h,left,/,~)+unbind(h,j,k,l,i,a,q,space)+change-prompt([I] /work/ )"},
		{"normal", protocol.Effect{Search: "off", Rebind: protocol.ModeNormal, Prompt: "[N] /work/ "},
			"disable-search+rebind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)+change-prompt([N] /work/ )"},
		{"add", protocol.Effect{Search: "on", Rebind: protocol.ModeAdd, Prompt: "[A] /work/ ", ClearQuery: true},
			"enable-search+unbind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)+change-prompt([A] /work/ )+clear-query"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RenderEffect(test.effect)
			if err != nil || got != test.want {
				t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
}

func TestRenderTerminalEffects(t *testing.T) {
	tests := []struct {
		name   string
		effect protocol.Effect
		want   string
	}{
		{"accept", protocol.Effect{Accept: true}, "print(enter)+accept"},
		{"normal escape", protocol.Effect{ClearMulti: true}, "clear-multi"},
		{"put", protocol.Effect{Put: "/"}, "put(/)"},
		{"ignore", protocol.Effect{Ignore: true}, "ignore"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RenderEffect(test.effect)
			if err != nil || got != test.want {
				t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
}

func TestActionArgumentsRejectControlsAndPutVocabulary(t *testing.T) {
	for _, raw := range []string{"x\rdown", "x\naccept", "x\x00y"} {
		if _, err := changePrompt(raw); err == nil {
			t.Fatalf("accepted control input %q", raw)
		}
	}
	for _, raw := range []string{"", ".", "x)+execute(id)"} {
		if _, err := put(raw); err == nil {
			t.Fatalf("accepted put input %q", raw)
		}
	}
}

func TestActionArgumentDelimiterCorpusCannotInjectAction(t *testing.T) {
	for _, raw := range []string{
		"x)+execute(id)", `x\\)+reload(e:en)`, "x,y:z", "{q}", "$(id)", "transform(e:en)", "accept+abort",
	} {
		action, err := changePrompt(raw)
		if err != nil {
			t.Fatalf("changePrompt(%q): %v", raw, err)
		}
		assertSingleActionArgument(t, action.text)
	}
}

func FuzzActionArgumentsRejectInjection(f *testing.F) {
	for _, seed := range []string{"ok", "x)+execute(id)", "x\rdown", "x\naccept", "x\x00y", `x\\)+reload(e:en)`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		action, err := changePrompt(raw)
		if strings.ContainsAny(raw, "\r\n\x00") {
			if err == nil {
				t.Fatalf("accepted control input %q as %q", raw, action.text)
			}
			return
		}
		if err == nil {
			assertSingleActionArgument(t, action.text)
		}
	})
}

func TestWindowsPromptBackslashIsEscapedOnlyForActionGrammar(t *testing.T) {
	got, err := changePrompt(`[N] C:\ `)
	if err != nil {
		t.Fatal(err)
	}
	if got.text != `change-prompt([N] C:\\ )` {
		t.Fatalf("action=%q", got.text)
	}
}

func assertSingleActionArgument(t *testing.T, rendered string) {
	t.Helper()
	depth, escaped, closes := 0, false, 0
	for _, r := range rendered {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closes++
			}
		case '+':
			if depth == 0 {
				t.Fatalf("action injection in %q", rendered)
			}
		}
		if depth < 0 {
			t.Fatalf("unbalanced action %q", rendered)
		}
	}
	if escaped || depth != 0 || closes != 1 {
		t.Fatalf("not one balanced action argument: %q", rendered)
	}
}
