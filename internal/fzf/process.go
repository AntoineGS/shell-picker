package fzf

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var executableBasename = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Config struct {
	Picker          protocol.Picker
	FZFPath         string
	ExecutablePath  string
	Environment     []string
	TracePath       string
	TraceSession    string
	CallbackAddress string
	CallbackToken   string
	ListenAPIKey    string
	Options         []string
	Input           io.ReadCloser
	Runner          process.Runner
	ForegroundTTY   *os.File
	TTYOut          io.Writer
	TTYErr          io.Writer
}

func Run(ctx context.Context, config Config) (Result, error) {
	spec, output, err := prepareSession(config)
	if err != nil {
		return Result{}, err
	}
	exitCode := 0
	if err := config.Runner.Run(ctx, spec); err != nil {
		var exitErr *process.ExitError
		if !errors.As(err, &exitErr) {
			return Result{}, fmt.Errorf("run fzf: %w", err)
		}
		exitCode = exitErr.ExitCode()
	}
	result, err := ParseOutput(config.Picker, output.Bytes(), exitCode)
	if err != nil {
		return Result{}, fmt.Errorf("parse fzf output: %w", err)
	}
	return result, nil
}

func prepareSession(config Config) (process.Spec, *bytes.Buffer, error) {
	if config.Input == nil {
		return process.Spec{}, nil, errors.New("fzf: nil input")
	}
	if config.FZFPath == "" {
		return process.Spec{}, nil, errors.New("fzf: empty fzf path")
	}
	if config.CallbackAddress == "" {
		return process.Spec{}, nil, errors.New("fzf: empty callback address")
	}
	if config.CallbackToken == "" {
		return process.Spec{}, nil, errors.New("fzf: empty callback token")
	}
	if !filepath.IsAbs(config.ExecutablePath) {
		return process.Spec{}, nil, errors.New("fzf: executable path must be absolute")
	}
	basename := filepath.Base(config.ExecutablePath)
	if !executableBasename.MatchString(basename) {
		return process.Spec{}, nil, fmt.Errorf("fzf: unsafe executable basename %q", basename)
	}
	directory := filepath.Dir(config.ExecutablePath)
	if strings.ContainsRune(directory, os.PathListSeparator) {
		return process.Spec{}, nil, errors.New("fzf: executable directory contains path-list separator")
	}
	if runtime.GOOS != "windows" && config.ForegroundTTY == nil {
		return process.Spec{}, nil, errors.New("fzf: foreground terminal is required")
	}
	listenAddress, err := listenAddressFromOptions(config.Options)
	if err != nil {
		return process.Spec{}, nil, err
	}
	if listenAddress == "" {
		if config.ListenAPIKey != "" {
			return process.Spec{}, nil, errors.New("fzf: listen API key requires a listen option")
		}
	} else {
		if config.ListenAPIKey == "" {
			return process.Spec{}, nil, errors.New("fzf: listen API key is required for listen mode")
		}
		if err := validateListenAPIKey(config.ListenAPIKey); err != nil {
			return process.Spec{}, nil, err
		}
	}

	sanitized := process.SanitizeEnv(config.Environment, nil)
	oldPath := environmentValue(sanitized, "PATH")
	path := directory + string(os.PathListSeparator) + oldPath
	controlled := map[string]string{
		"PATH":               path,
		"SHELL_PICKER_ADDR":  config.CallbackAddress,
		"SHELL_PICKER_TOKEN": config.CallbackToken,
	}
	if config.TracePath != "" && config.TraceSession != "" {
		controlled["SHELL_PICKER_TRACE_PATH"] = config.TracePath
		controlled["SHELL_PICKER_TRACE_SESSION"] = config.TraceSession
	}
	if listenAddress != "" {
		controlled["FZF_API_KEY"] = config.ListenAPIKey
	}
	environment := process.SanitizeEnv(sanitized, controlled)
	args := append([]string(nil), config.Options...)
	args = append(args, "--with-shell="+basename+" --fzf-shell")
	output := &bytes.Buffer{}
	stderr := config.TTYErr
	if stderr == nil && config.ForegroundTTY != nil {
		stderr = config.ForegroundTTY
	}
	return process.Spec{
		Path:             config.FZFPath,
		Args:             args,
		Env:              environment,
		Stdin:            config.Input,
		Stdout:           output,
		Stderr:           stderr,
		CloseStdinOnExit: true,
		Containment:      process.ContainmentForegroundTree,
		ForegroundTTY:    config.ForegroundTTY,
		WaitDelay:        time.Second,
	}, output, nil
}

func listenAddressFromOptions(options []string) (string, error) {
	var address string
	for _, option := range options {
		if option == "--listen" {
			return "", errInvalidListenAddress
		}
		if !strings.HasPrefix(option, "--listen=") {
			continue
		}
		if address != "" {
			return "", errors.New("fzf: multiple listen options")
		}
		address = strings.TrimPrefix(option, "--listen=")
		if err := validateListenAddress(address); err != nil {
			return "", err
		}
	}
	return address, nil
}

func validateListenAPIKey(key string) error {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return errors.New("fzf: invalid listen API key")
	}
	return nil
}

func environmentValue(environment []string, wanted string) string {
	value := ""
	for _, entry := range environment {
		key, candidate, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == wanted || runtime.GOOS == "windows" && strings.EqualFold(key, wanted) {
			value = candidate
		}
	}
	return value
}

func CheckVersion(ctx context.Context, runner process.Runner, path string) error {
	_, err := CheckVersionWithEnvironment(ctx, runner, path, os.Environ())
	return err
}

// CheckVersionWithEnvironment executes one version probe and returns the validated version from those same bytes.
func CheckVersionWithEnvironment(ctx context.Context, runner process.Runner, path string, environment []string) (string, error) {
	if path == "" {
		return "", errors.New("fzf: empty fzf path")
	}
	var stdout, stderr bytes.Buffer
	err := runner.Run(ctx, process.Spec{
		Path:        path,
		Args:        []string{"--version"},
		Env:         process.SanitizeEnv(environment, nil),
		Stdout:      &stdout,
		Stderr:      &stderr,
		Containment: process.ContainmentOwnTree,
		WaitDelay:   time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("check fzf version: %w", err)
	}
	version := strings.Fields(stdout.String())
	major, minor, patch, err := parseVersion(stdout.String())
	if err != nil {
		return "", err
	}
	if major == 0 && (minor < 74 || minor == 74 && patch < 1) {
		return version[0], fmt.Errorf("fzf: version %d.%d.%d is below required 0.74.1", major, minor, patch)
	}
	return version[0], nil
}

func parseVersion(output string) (int, int, int, error) {
	version := strings.Fields(output)
	if len(version) == 0 {
		return 0, 0, 0, errors.New("fzf: empty version output")
	}
	parts := strings.Split(version[0], ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("fzf: malformed version %q", version[0])
	}
	values := [3]int{}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return 0, 0, 0, fmt.Errorf("fzf: malformed version %q", version[0])
		}
		values[i] = value
	}
	return values[0], values[1], values[2], nil
}
