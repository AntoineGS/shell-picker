package integration

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func copyExecutable(source, destination string) error {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if err := copyExecutableInto(source, output); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

func copyExecutableInto(source string, output *os.File) error {
	input, inputErr := os.Open(source)
	var copyErr, inputCloseErr error
	if inputErr == nil {
		_, copyErr = io.Copy(output, input)
		inputCloseErr = input.Close()
	}
	syncErr := output.Sync()
	chmodErr := output.Chmod(0o700)
	closeErr := output.Close()
	return errors.Join(inputErr, copyErr, inputCloseErr, syncErr, chmodErr, closeErr)
}

func buildPerformanceZoxideHelper(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate benchmark helper source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), ".."))
	output := filepath.Join(t.TempDir(), binaryName("performance-zoxide"))
	arguments := append([]string{"build"}, firstFrameReproducibleBuildFlags...)
	arguments = append(arguments, "-o", output, "./integration/testhelper/performance")
	command := exec.Command("go", arguments...)
	command.Dir = repository
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build performance zoxide helper: %v\n%s", err, data)
	}
	if err := os.Chmod(output, 0o700); err != nil {
		t.Fatalf("chmod performance zoxide helper: %v", err)
	}
	return output
}

func warmPerformanceZoxideHelper(t *testing.T, path string) {
	t.Helper()
	command := exec.Command(path, "query", "--list")
	command.Env = replaceEnvironment(os.Environ(), "GO_PERF_ZOXIDE_MODE=empty")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		t.Fatalf("warm performance zoxide helper: %v", err)
	}
}
