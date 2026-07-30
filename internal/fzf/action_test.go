package fzf

import (
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRenderNavigationEffectEndsWithHeader(t *testing.T) {
	effect := protocol.Effect{Mode: protocol.ModeNormal, Prompt: "[N] ", Header: `a\)b/`,
		ClearMulti: true, ClearQuery: true, ReloadGeneration: 7}
	got, err := RenderEffect(effect)
	want := `clear-multi+reload-sync(l:7)+clear-query+wait+first+change-prompt([N] )+change-header:a\)b/`
	if err != nil || got != want {
		t.Fatalf("got=%q want=%q err=%v", got, want, err)
	}
}

func TestModePromptActionAcceptsOnlyClosedVocabulary(t *testing.T) {
	for _, prompt := range []string{"[I] ", "[N] ", "[A] ", "[A!] "} {
		if _, err := changeModePrompt(prompt); err != nil {
			t.Fatalf("prompt %q: %v", prompt, err)
		}
	}
	for _, prompt := range []string{"", "[I] > ", "[I] /work/ ", "x)+abort"} {
		if _, err := changeModePrompt(prompt); err == nil {
			t.Fatalf("accepted prompt %q", prompt)
		}
	}
}

func TestRenderModeEffects(t *testing.T) {
	tests := []struct {
		name   string
		effect protocol.Effect
		want   string
	}{
		{"insert", protocol.Effect{Search: "on", Rebind: protocol.ModeInsert, Prompt: "[I] ", Header: "/work/"},
			"enable-search+rebind(ctrl-l,tab,right,ctrl-h,left,/,~)+unbind(h,j,k,l,i,a,q,space)+change-prompt([I] )+change-header:/work/"},
		{"normal", protocol.Effect{Search: "off", Rebind: protocol.ModeNormal, Prompt: "[N] ", Header: "/work/"},
			"disable-search+rebind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)+change-prompt([N] )+change-header:/work/"},
		{"add", protocol.Effect{Search: "on", Rebind: protocol.ModeAdd, Prompt: "[A] ", Header: "/work/", ClearQuery: true},
			"enable-search+unbind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)+clear-query+change-prompt([A] )+change-header:/work/"},
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
		if _, err := changeHeader(raw); err == nil {
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
		action, err := changeHeader(raw)
		if err != nil {
			t.Fatalf("changeHeader(%q): %v", raw, err)
		}
		assertTerminalHeaderAction(t, action.text, raw)
	}
}

func FuzzActionArgumentsRejectInjection(f *testing.F) {
	for _, seed := range []string{"ok", "x)+execute(id)", "x\rdown", "x\naccept", "x\x00y", `x\\)+reload(e:en)`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		action, err := changeHeader(raw)
		if strings.ContainsAny(raw, "\r\n\x00") {
			if err == nil {
				t.Fatalf("accepted control input %q as %q", raw, action.text)
			}
			return
		}
		if err == nil {
			assertTerminalHeaderAction(t, action.text, raw)
		}
	})
}

func TestWindowsPromptBackslashIsPreservedByTerminalAction(t *testing.T) {
	got, err := changeHeader(`C:\ `)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalHeaderAction(t, got.text, `C:\ `)
}

func assertTerminalHeaderAction(t *testing.T, rendered, header string) {
	t.Helper()
	if want := "change-header:" + header; rendered != want {
		t.Fatalf("action=%q want terminal action=%q", rendered, want)
	}
}
