package callback

import "testing"

func TestParseAcceptsOnlyStaticGrammar(t *testing.T) {
	accepted := []string{"e:mi", "e:ma", "e:es", "e:fw", "e:up", "e:sl", "e:hm", "e:en", "l:1", "l:18446744073709551615", "p"}
	rejected := []string{"", "e:q", "l:0", "l:-1", "l:01", "l:18446744073709551616", "p x", "e:en;id", "$(id)", "sh -c id", "p\x00x"}
	for _, raw := range accepted {
		if _, err := Parse(raw); err != nil {
			t.Fatalf("rejected %q: %v", raw, err)
		}
	}
	for _, raw := range rejected {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
