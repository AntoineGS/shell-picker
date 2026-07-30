package callback

import (
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestParseAcceptsOnlyStaticGrammar(t *testing.T) {
	accepted := map[string]Command{
		"e:mi":                   {Kind: KindEvent, Opcode: protocol.OpModeInsert},
		"e:ma":                   {Kind: KindEvent, Opcode: protocol.OpModeAdd},
		"e:es":                   {Kind: KindEvent, Opcode: protocol.OpEscape},
		"e:fw":                   {Kind: KindEvent, Opcode: protocol.OpForward},
		"e:up":                   {Kind: KindEvent, Opcode: protocol.OpParent},
		"e:sl":                   {Kind: KindEvent, Opcode: protocol.OpSlash},
		"e:hm":                   {Kind: KindEvent, Opcode: protocol.OpHome},
		"e:en":                   {Kind: KindEvent, Opcode: protocol.OpEnter},
		"l:1":                    {Kind: KindLoad, Generation: 1},
		"l:18446744073709551615": {Kind: KindLoad, Generation: ^uint64(0)},
		"p":                      {Kind: KindPreview},
		"d":                      {Kind: KindDisplay},
		"i:cd":                   {Kind: KindInfo, Picker: protocol.PickerCD},
		"i:cp":                   {Kind: KindInfo, Picker: protocol.PickerCP},
	}
	rejected := []string{"", "e:q", "l:0", "l:-1", "l:01", "l:18446744073709551616", "p x", "e:en;id",
		"$(id)", "sh -c id", "p\x00x", "i", "i:CD", "i:other", "d:x", " d", "d ", "i:cp;id"}
	for raw, want := range accepted {
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("rejected %q: %v", raw, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Parse(%q)=%+v want %+v", raw, got, want)
		}
	}
	for _, raw := range rejected {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
