package release

import (
	"bytes"
	"debug/elf"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func verifyBinary(archive, name string, data []byte, version string) {
	workspace, err := os.Getwd()
	if err != nil {
		fatal(err.Error())
	}
	if bytes.Contains(data, []byte(workspace)) || !bytes.Contains(data, []byte(version)) || bytes.Contains(data, []byte("shell-picker dev")) {
		fatal(fmt.Sprintf("invalid version or workspace path in %s", archive))
	}
	base := filepath.Base(archive)
	if strings.Contains(base, "_linux_") {
		file, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			fatal(err.Error())
		}
		want := elf.EM_X86_64
		if strings.Contains(base, "_arm64.") {
			want = elf.EM_AARCH64
		}
		if file.Machine != want {
			fatal("wrong ELF architecture")
		}
	} else {
		file, err := pe.NewFile(bytes.NewReader(data))
		if err != nil {
			fatal(err.Error())
		}
		defer func() {
			if err := file.Close(); err != nil {
				fatal(err.Error())
			}
		}()
		want := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
		if strings.Contains(base, "_arm64.") {
			want = pe.IMAGE_FILE_MACHINE_ARM64
		}
		if file.FileHeader.Machine != want {
			fatal("wrong PE architecture")
		}
	}
	if name == "shell-picker" && strings.Contains(base, "_linux_amd64.") {
		temporary, err := os.CreateTemp("", "shell-picker-version-")
		if err != nil {
			fatal(err.Error())
		}
		path := temporary.Name()
		defer func() {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				fatal(err.Error())
			}
		}()
		if _, err := temporary.Write(data); err != nil {
			fatal(err.Error())
		}
		if err := temporary.Chmod(0o755); err != nil {
			fatal(err.Error())
		}
		if err := temporary.Close(); err != nil {
			fatal(err.Error())
		}
		output, err := exec.Command(path, "version").Output()
		if err != nil || string(output) != "shell-picker "+version+"\n" {
			fatal("injected version execution failed")
		}
	}
}
