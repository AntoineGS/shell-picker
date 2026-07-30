package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		os.Exit(10)
	}
	root := filepath.Dir(executable)
	if os.Getenv("TASK20_RESOURCE_GRANDCHILD") == "1" {
		if err := appendProcess(root, "grandchild"); err != nil {
			os.Exit(11)
		}
		waitForTermination()
		return
	}
	if err := writeEnvironment(root); err != nil {
		os.Exit(12)
	}
	if len(os.Args) == 0 {
		os.Exit(13)
	}
	if strings.HasPrefix(filepath.Base(executable), "pdftoppm") {
		artifact := os.Args[len(os.Args)-1] + ".jpg"
		if err := os.WriteFile(artifact, []byte("task20-artifact\n"), 0o600); err != nil {
			os.Exit(14)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "block")); err != nil {
		return
	}
	if err := appendProcess(root, "renderer"); err != nil {
		os.Exit(15)
	}
	child := exec.Command(executable)
	child.Env = append(os.Environ(), "TASK20_RESOURCE_GRANDCHILD=1")
	if err := child.Start(); err != nil {
		os.Exit(16)
	}
	waitForTermination()
}

func writeEnvironment(root string) error {
	environment := append([]string(nil), os.Environ()...)
	sort.Strings(environment)
	return os.WriteFile(filepath.Join(root, "environment.log"), []byte(strings.Join(environment, "\n")+"\n"), 0o600)
}

func appendProcess(root, role string) error {
	file, err := os.OpenFile(filepath.Join(root, "processes.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "%s %d\n", role, os.Getpid())
	return errorsJoin(writeErr, file.Close())
}

func waitForTermination() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	<-signals
}

func errorsJoin(first, second error) error {
	if first != nil {
		return first
	}
	return second
}
