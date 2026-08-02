package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/integration/windowsnative"
)

func filteredGoEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOENV") || strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "windowsnative accepts no arguments")
		os.Exit(2)
	}
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "windowsnative requires GOOS=windows at runtime")
		os.Exit(2)
	}
	if err := windowsnative.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid Windows manifest: %v\n", err)
		os.Exit(2)
	}
	for _, pkg := range windowsnative.Packages {
		command := exec.Command("go", "test", pkg.Path, "-run", pkg.Pattern, "-count=1", "-timeout=5m")
		command.Env = append(filteredGoEnvironment(os.Environ()), "GOENV=off", "GOFLAGS=")
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", pkg.Path, err)
			os.Exit(1)
		}
	}
}
