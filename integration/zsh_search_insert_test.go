package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestZshForcedSearchInsertsWithoutExecuting(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	plugin, err := filepath.Abs("../adapters/zsh/shell-picker.plugin.zsh")
	if err != nil {
		t.Fatal(err)
	}
	for _, widget := range []string{"cd", "cp", "search", "files_search"} {
		t.Run(widget, func(t *testing.T) {
			root := t.TempDir()
			fake := "#!/bin/sh\nprintf '/tmp/a b\\000/tmp/c\\000'\nexit 10\n"
			if widget == "search" || widget == "files_search" {
				fake = strings.Replace(fake, "exit 10", "exit 0", 1)
			}
			if widget == "files_search" {
				fake = strings.Replace(fake, "#!/bin/sh\n", "#!/bin/sh\ncase \"$*\" in *'--search-scope files'*) ;; *) exit 2 ;; esac\n", 1)
			}
			if err := os.WriteFile(filepath.Join(root, "shell-picker"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			script := `
source "$PLUGIN"
integer accepted=0
zle() { [[ $1 == accept-line ]] && (( ++accepted )); return 0 }
BUFFER='cd '; CURSOR=3; LBUFFER='cd '; RBUFFER=''
_shell_picker_$WIDGET
print -r -- "$accepted"
print -r -- "$LBUFFER"
`
			command := exec.Command(zsh, "-f", "-c", script)
			command.Env = append(os.Environ(), "PLUGIN="+plugin, "WIDGET="+widget, "PATH="+root+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if err != nil || string(output) != "0\ncd /tmp/a\\ b /tmp/c\n" {
				t.Fatalf("insertion=%q err=%v", output, err)
			}
		})
	}
}
