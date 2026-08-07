package fzf

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestPickerOptions(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		config := OptionsConfig{Picker: picker, Prompt: "[I] ", Header: "/work/"}
		got, err := Options(config)
		if err != nil {
			t.Fatalf("Options(%+v) error = %v", config, err)
		}
		want := wantOptionSnapshot(config)
		if !slices.Equal(got, want) {
			t.Fatalf("Options(%+v) = %q, want %q", config, got, want)
		}
	}
}

func TestPickerOptionsSidecarSnapshot(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		config := OptionsConfig{
			Picker:        picker,
			Prompt:        "[I] ",
			Header:        "/work/",
			ListenAddress: "127.0.0.1:4321",
		}
		got, err := Options(config)
		if err != nil {
			t.Fatalf("Options(%+v) error = %v", config, err)
		}
		want := wantOptionSnapshot(config)
		if !slices.Equal(got, want) {
			t.Fatalf("Options(%+v) = %q, want %q", config, got, want)
		}
	}
}

func TestPickerOptionsWindowsUseNativePresentation(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		config := OptionsConfig{Picker: picker, Prompt: "[I] ", Header: "/work/"}
		got, err := optionsForPlatform(config, "windows")
		if err != nil {
			t.Fatalf("optionsForPlatform(%+v, windows) error = %v", config, err)
		}
		want := wantOptionSnapshotForPlatform(config, "windows")
		if !slices.Equal(got, want) {
			t.Fatalf("optionsForPlatform(%+v, windows) = %q, want %q", config, got, want)
		}
	}
}

func TestPickerOptionsLinuxRetainsCustomPresentation(t *testing.T) {
	for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
		config := OptionsConfig{Picker: picker, Prompt: "[I] ", Header: "/work/"}
		got, err := optionsForPlatform(config, "linux")
		if err != nil {
			t.Fatalf("optionsForPlatform(%+v, linux) error = %v", config, err)
		}
		want := wantOptionSnapshotForPlatform(config, "linux")
		if !slices.Equal(got, want) {
			t.Fatalf("optionsForPlatform(%+v, linux) = %q, want %q", config, got, want)
		}
	}
}

func TestPickerOptionsExplicitSidecarProfilePrecedesPlatformProfile(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
			config := OptionsConfig{
				Picker:        picker,
				Prompt:        "[I] ",
				Header:        "/work/",
				ListenAddress: "127.0.0.1:4321",
			}
			got, err := optionsForPlatform(config, goos)
			if err != nil {
				t.Fatalf("optionsForPlatform(%+v, %s) error = %v", config, goos, err)
			}
			want := wantOptionSnapshotForPlatform(config, goos)
			if !slices.Equal(got, want) {
				t.Fatalf("optionsForPlatform(%+v, %s) = %q, want %q", config, goos, got, want)
			}
		}
	}
}

func TestPickerOptionsWindowsPreserveDefaultNavigation(t *testing.T) {
	options, err := optionsForPlatform(OptionsConfig{Picker: protocol.PickerCP}, "windows")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--bind=ctrl-l,tab,right:transform(e:fw)",
		"--bind=l:trigger(tab)",
	} {
		if !slices.Contains(options, want) {
			t.Fatalf("Windows default options lack %q: %q", want, options)
		}
	}
	for _, forbidden := range []string{
		"--bind=ctrl-l,right:transform(e:fw)",
		"--bind=l:trigger(right)",
	} {
		if slices.Contains(options, forbidden) {
			t.Fatalf("Windows default options contain sidecar navigation %q: %q", forbidden, options)
		}
	}
}

func TestPickerOptionsRejectListenAddressInjection(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:0",
		"127.0.0.1:01",
		"127.0.0.1:65536",
		"127.0.0.1:-1",
		"127.0.0.1:1\n--bind=q:abort",
		"127.0.0.1:1,change-header:forged",
		"127.0.0.1:1:2",
		"127.0.0.2:1",
		"localhost:1",
		"127.0.0.1:١",
	} {
		config := OptionsConfig{Picker: protocol.PickerCP, ListenAddress: address}
		if got, err := Options(config); err == nil {
			t.Fatalf("Options(%+v) = %q, nil error; want invalid-address error", config, got)
		}
	}
}

func TestPickerOptionsAcceptCanonicalListenAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1", "127.0.0.1:65535"} {
		config := OptionsConfig{Picker: protocol.PickerCD, ListenAddress: address}
		if _, err := Options(config); err != nil {
			t.Fatalf("Options(%+v) error = %v", config, err)
		}
	}
}

func TestPickerOptionsSidecarUsesTabForFZFSelectionAndKeepsForwardNavigationOnRight(t *testing.T) {
	options := mustOptions(t, OptionsConfig{Picker: protocol.PickerCP, ListenAddress: "127.0.0.1:4321"})
	if !slices.Contains(options, "--bind=ctrl-l,right:transform(e:fw)") {
		t.Fatalf("sidecar options do not retain forward navigation on right: %q", options)
	}
	if !slices.Contains(options, "--bind=l:trigger(right)") {
		t.Fatalf("sidecar options do not map normal l to right navigation: %q", options)
	}
	for _, option := range options {
		if strings.Contains(option, "transform(d)") || option == "--bind=resize:transform(d)" {
			t.Fatalf("sidecar options still invoke the display callback: %q", option)
		}
	}
	for _, option := range options {
		if strings.HasPrefix(option, "--bind=ctrl-l,tab,right:") || option == "--bind=l:trigger(tab)" {
			t.Fatalf("sidecar options still bind Tab to navigation: %q", option)
		}
	}
}

func TestNormalModeIgnoresEveryOtherASCIIPrintableKey(t *testing.T) {
	active := map[rune]bool{
		'h': true, 'j': true, 'k': true, 'l': true, 'i': true, 'a': true, 'q': true,
		'g': true, 'G': true,
		'/': true, '~': true, ',': true, '.': true,
	}
	options := mustOptions(t, OptionsConfig{Picker: protocol.PickerCD, Prompt: "[I] ", Header: "/work/"})
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
		options := mustOptions(t, OptionsConfig{Picker: picker, Prompt: "prompt", Header: "header"})
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

func mustOptions(t *testing.T, config OptionsConfig) []string {
	t.Helper()
	options, err := Options(config)
	if err != nil {
		t.Fatalf("Options(%+v) error = %v", config, err)
	}
	return options
}

func wantOptionSnapshot(config OptionsConfig) []string {
	return wantOptionSnapshotForPlatform(config, runtime.GOOS)
}

func wantOptionSnapshotForPlatform(config OptionsConfig, goos string) []string {
	options := []string{
		"--ansi",
		"--style=full",
		"--layout=reverse",
		"--delimiter=\t",
		"--with-nth=2",
		"--keep-right",
		"--jump-labels=g",
		"--read0",
		"--print0",
		"--prompt=" + config.Prompt,
		"--header=" + config.Header,
		"--header-first",
	}
	if config.ListenAddress == "" {
		if config.Picker == protocol.PickerCD {
			if goos == "windows" {
				options = append(options, "--info=inline-right")
			} else {
				options = append(options, "--info-command=i:cd")
			}
		} else {
			if goos == "windows" {
				options = append(options, "--info=inline-right")
			} else {
				options = append(options, "--info-command=i:cp")
			}
		}
	} else {
		options = append(options, "--info=hidden", "--list-label=0/0", "--listen="+config.ListenAddress)
	}
	forwardBinding, normalForward := "ctrl-l,tab,right", "tab"
	if config.ListenAddress != "" {
		forwardBinding, normalForward = "ctrl-l,right", "right"
	}
	options = append(options,
		"--preview=p",
		"--preview-window=right:50%:wrap:<80(down:50%:wrap)",
		"--bind=enter:transform(e:en)",
		"--bind=esc:transform(e:es)",
		"--bind=i:transform(e:mi)",
		"--bind=a:transform(e:ma)",
		"--bind="+forwardBinding+":transform(e:fw)",
		"--bind=ctrl-h,left:transform(e:up)",
		"--bind=/:transform(e:sl)",
		"--bind=~:transform(e:hm)",
		"--bind=g:jump",
		"--bind=G:last",
		"--bind=jump:first",
		"--bind=j:down",
		"--bind=k:up",
		"--bind=h:trigger(ctrl-h)",
		"--bind=l:trigger("+normalForward+")",
		"--bind=q:cancel+abort",
		"--bind=ctrl-u:half-page-up",
		"--bind=ctrl-d:half-page-down",
		"--bind=,:preview-half-page-up",
		"--bind=.:preview-half-page-down",
		"--bind=change:transform(e:rs)",
		"--bind=result-final:rebind(change)+unbind(result-final)",
	)
	start := "--bind=start:" + startUnbind().text
	if config.ListenAddress == "" && goos != "windows" {
		start += "+transform(d)"
	}
	options = append(options, start)
	if config.ListenAddress == "" && goos != "windows" {
		options = append(options, "--bind=resize:transform(d)")
	}
	for _, key := range optionSnapshotPrintableKeys() {
		options = append(options, "--bind="+key+":ignore")
	}
	if config.Picker == protocol.PickerCD {
		return append(options, "--bind=space:clear-multi+toggle", "--sort", "--print-query", "--multi=1")
	}
	return append(options, "--bind=space:toggle", "--no-sort", "--multi")
}

func optionSnapshotPrintableKeys() []string {
	return []string{
		"!", `"`, "#", "$", "%", "&", "'", "(", ")", "*", "+", "-",
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", ":", ";", "<", "=", ">", "?", "@",
		"A", "B", "C", "D", "E", "F", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z",
		"[", `\`, "]", "^", "_", "`", "b", "c", "d", "e", "f", "m", "n", "o", "p", "r", "s", "t", "u", "v", "w", "x", "y", "z", "{", "|", "}",
	}
}
