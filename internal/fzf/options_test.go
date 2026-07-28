package fzf

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestPickerOptions(t *testing.T) {
	common := []string{
		"--ansi", "--style=full", "--layout=reverse", "--delimiter=\t", "--with-nth=2", "--read0", "--print0",
		"--prompt=[I] /work/ ", "--preview=p", "--preview-window=right:50%:wrap",
		"--bind=enter:transform(e:en)", "--bind=esc:transform(e:es)", "--bind=i:transform(e:mi)",
		"--bind=a:transform(e:ma)", "--bind=ctrl-l,tab,right:transform(e:fw)",
		"--bind=ctrl-h,left:transform(e:up)", "--bind=/:transform(e:sl)", "--bind=~:transform(e:hm)",
		"--bind=j:down", "--bind=k:up", "--bind=h:trigger(ctrl-h)", "--bind=l:trigger(tab)", "--bind=q:abort",
		"--bind=start:unbind(h,j,k,l,i,a,q,space)",
	}
	cdWant := append(append([]string{}, common...), "--bind=space:clear-multi+toggle", "--sort", "--print-query", "--multi=1")
	cpWant := append(append([]string{}, common...), "--bind=space:toggle", "--no-sort", "--multi")
	if got := Options(protocol.PickerCD, "[I] /work/ "); !reflect.DeepEqual(got, cdWant) {
		t.Fatalf("cd options:\n got=%q\nwant=%q", got, cdWant)
	}
	if got := Options(protocol.PickerCP, "[I] /work/ "); !reflect.DeepEqual(got, cpWant) {
		t.Fatalf("cp options:\n got=%q\nwant=%q", got, cpWant)
	}
}

func TestOptionsHaveNoListenOrDuplicateBindings(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		options := Options(picker, "prompt")
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
