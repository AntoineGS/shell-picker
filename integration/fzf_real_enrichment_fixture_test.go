package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const (
	realZoxideHelperMode          = "real-zoxide-enrichment"
	realZoxideStartedEnvironment  = "REAL_FZF_ZOXIDE_STARTED"
	realZoxideReleaseEnvironment  = "REAL_FZF_ZOXIDE_RELEASE"
	realZoxideRootEnvironment     = "REAL_FZF_ZOXIDE_ROOT"
	realZoxideReleasePollInterval = 10 * time.Millisecond
	realZoxideHelperDeadline      = 2 * time.Minute
	promptReturnSentinel          = "SHELL_PICKER_PROMPT_RETURN_TASK8"
)

var keyRight = []byte{0x1b, '[', 'C'}

type realZoxideToolCache struct {
	mu     sync.Mutex
	root   string
	source string
}

var realZoxideTools realZoxideToolCache

func (cache *realZoxideToolCache) copyTo(rebuildSource, destination string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	source, err := cache.ensureLocked(rebuildSource)
	if err != nil {
		return err
	}
	return copyExecutable(source, destination)
}

func (cache *realZoxideToolCache) ensureLocked(rebuildSource string) (string, error) {
	if cache.root == "" {
		root, err := os.MkdirTemp("", "shell-picker-real-zoxide-tools-")
		if err != nil {
			return "", err
		}
		cache.root = root
	}
	if err := os.MkdirAll(cache.root, 0o700); err != nil {
		return "", err
	}
	if cache.source == "" {
		cache.source = filepath.Join(cache.root, binaryName("zoxide"))
	}
	if err := validateRealZoxideExecutable(cache.source); err == nil {
		return cache.source, nil
	}
	if err := validateRealZoxideExecutable(rebuildSource); err != nil {
		return "", fmt.Errorf("zoxide helper source unavailable (test binary may be quarantined): %w", err)
	}
	if err := rebuildRealZoxideExecutable(rebuildSource, cache.root, cache.source); err != nil {
		return "", fmt.Errorf("rebuild zoxide helper source: %w", err)
	}
	if err := validateRealZoxideExecutable(cache.source); err != nil {
		return "", fmt.Errorf("validate rebuilt zoxide helper source: %w", err)
	}
	return cache.source, nil
}

func validateRealZoxideExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if info.Size() == 0 {
		return fmt.Errorf("empty file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("file is not executable")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func rebuildRealZoxideExecutable(source, directory, destination string) error {
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(destination)+".rebuild-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := copyExecutableInto(source, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		if removeErr := os.Remove(destination); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		if err := os.Rename(temporaryName, destination); err != nil {
			return err
		}
	}
	removeTemporary = false
	return nil
}

func waitForRealZoxideRelease(ctx context.Context, releasePath string) error {
	if ctx == nil {
		return errors.New("real zoxide release: nil context")
	}
	ticker := time.NewTicker(realZoxideReleasePollInterval)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(releasePath); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

type realZoxideFixture struct {
	*realFZFFixture
	tools                   string
	started, release        string
	lateTarget, otherTarget string
	promptWrapper           string
}

func newRealZoxideFixture(t *testing.T, fzfPath string) *realZoxideFixture {
	t.Helper()
	base := newRealFZFFixture(t, fzfPath, "async zoxide enrichment")
	fixture := &realZoxideFixture{
		realFZFFixture: base,
		tools:          filepath.Join(base.root, "async zoxide tools"),
		started:        filepath.Join(base.root, "zoxide-started"),
		release:        filepath.Join(base.root, "zoxide-release"),
		lateTarget:     filepath.Join(base.home, "late-match-target"),
		otherTarget:    filepath.Join(base.home, "other-target"),
	}
	if err := os.MkdirAll(fixture.tools, 0o700); err != nil {
		t.Fatal(err)
	}
	toolTarget := filepath.Join(fixture.tools, binaryName("zoxide"))
	harness, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := realZoxideTools.copyTo(harness, toolTarget); err != nil {
		t.Fatalf("prepare zoxide helper (test binary may be quarantined): %v", err)
	}
	for _, path := range []string{fixture.lateTarget, fixture.otherTarget} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixture.ReplaceLocalCandidates(t, "local-visible")
	return fixture
}

func (fixture *realZoxideFixture) ReplaceLocalCandidates(t *testing.T, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(fixture.cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(fixture.cwd, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(fixture.cwd, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func (fixture *realZoxideFixture) StartCD(t *testing.T) terminalSession {
	t.Helper()
	return fixture.startCD(t, fixture.picker, nil)
}

func (fixture *realZoxideFixture) StartCDWithPromptReturn(t *testing.T) terminalSession {
	t.Helper()
	if fixture.promptWrapper == "" {
		_, fixture.promptWrapper = cachedRealBinaries(t)
	}
	return fixture.startCD(t, fixture.promptWrapper, []string{"prompt-return", fixture.picker, promptReturnSentinel})
}

func (fixture *realZoxideFixture) StartBlockedCD(t *testing.T, promptReturn bool, visible string) (terminalSession, ownedProcessIdentity) {
	t.Helper()
	var term terminalSession
	if promptReturn {
		term = fixture.StartCDWithPromptReturn(t)
	} else {
		term = fixture.StartCD(t)
	}
	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close blocked zoxide picker: %v", err)
		}
	})
	pending := term.WaitBarrier(testContext(t), barrier{Event: "generation.publish", Generation: 1, Count: 1})
	assertRealZoxidePendingPublication(t, pending)
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	waitForTerminalText(t, term, visible)
	identity := fixture.WaitZoxideStart(t)
	t.Cleanup(func() {
		if err := identity.Close(); err != nil {
			t.Errorf("close zoxide process identity %d: %v", identity.PID(), err)
		}
	})
	return term, identity
}

func (fixture *realZoxideFixture) startCD(t *testing.T, executable string, prefixArgs []string) terminalSession {
	t.Helper()
	environment := replaceEnvironment(os.Environ(),
		"FZF_DEFAULT_OPTS=--bind=start:abort", "FZF_DEFAULT_COMMAND=printf forged",
		"SHELL_PICKER_ADDR=http://127.0.0.1:1", "SHELL_PICKER_TOKEN=forged", "TERM=xterm-256color",
		"PATH="+fixture.tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		parityHelperEnvironment+"="+realZoxideHelperMode,
		realZoxideStartedEnvironment+"="+fixture.started,
		realZoxideReleaseEnvironment+"="+fixture.release,
		realZoxideRootEnvironment+"="+fixture.home,
	)
	args := append(append([]string(nil), prefixArgs...), string(protocol.PickerCD), "--cwd", fixture.cwd, "--home", fixture.home,
		"--fzf", fixture.fzf, "--zoxide-policy", "cached", "--zoxide-timeout", "0")
	return newTerminalSession(t, terminalConfig{Path: executable, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: 120, Lines: 35})
}

func (fixture *realZoxideFixture) WaitZoxideStart(t *testing.T) ownedProcessIdentity {
	t.Helper()
	ctx := testContext(t)
	data, err := waitForRealZoxideFile(ctx, fixture.started)
	if err != nil {
		t.Fatalf("wait for zoxide start marker: %v", err)
	}
	pid, marker, err := parseRealZoxideIdentity(data)
	if err != nil {
		t.Fatalf("zoxide start marker=%q: %v", data, err)
	}
	identity, err := openOwnedProcessIdentity(pid)
	if err != nil {
		t.Fatalf("open zoxide process identity %d: %v", pid, err)
	}
	if err := verifyProcessIdentityMarker(identity, marker); err != nil {
		_ = identity.Close()
		t.Fatalf("verify zoxide process identity %d: %v", pid, err)
	}
	if err := verifyOwnedProcessGroup(pid); err != nil {
		_ = identity.Close()
		t.Fatalf("verify zoxide process group %d: %v", pid, err)
	}
	return identity
}

func TestParseRealZoxideIdentityRequiresAnInstanceToken(t *testing.T) {
	pid, token, err := parseRealZoxideIdentity([]byte("1234\ninstance-token\n"))
	if err != nil || pid != 1234 || token != "instance-token" {
		t.Fatalf("identity=%d/%q err=%v", pid, token, err)
	}
	for _, raw := range []string{"1234\n", "0\ninstance-token\n", "bad\ninstance-token\n", "1234\ninstance-token\nextra\n"} {
		if pid, token, err := parseRealZoxideIdentity([]byte(raw)); err == nil || pid != 0 || token != "" {
			t.Fatalf("parseRealZoxideIdentity(%q)=%d/%q/%v, want rejection", raw, pid, token, err)
		}
	}
}

func parseRealZoxideIdentity(data []byte) (int, string, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) != 3 || lines[2] != "" || lines[0] == "" || lines[1] == "" {
		return 0, "", errors.New("real zoxide identity: malformed marker")
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil || pid <= 0 {
		return 0, "", errors.New("real zoxide identity: invalid pid")
	}
	return pid, lines[1], nil
}

func (fixture *realZoxideFixture) Release(t *testing.T) {
	t.Helper()
	if err := writeRealZoxideMarker(fixture.release, "release\n"); err != nil {
		t.Fatalf("release zoxide helper: %v", err)
	}
}

func waitForRealZoxideFile(ctx context.Context, path string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("real zoxide file: nil context")
	}
	ticker := time.NewTicker(realZoxideReleasePollInterval)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

func writeRealZoxideMarker(path, contents string) error {
	if path == "" {
		return errors.New("real zoxide marker: empty path")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func runRealZoxideHelper() int {
	if len(os.Args) != 3 || os.Args[1] != "query" || os.Args[2] != "--list" {
		return 2
	}
	marker, err := currentProcessIdentityMarker()
	if err != nil {
		return 3
	}
	if err := writeRealZoxideMarker(os.Getenv(realZoxideStartedEnvironment), fmt.Sprintf("%d\n%s\n", os.Getpid(), marker)); err != nil {
		return 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), realZoxideHelperDeadline)
	defer cancel()
	if err := waitForRealZoxideRelease(ctx, os.Getenv(realZoxideReleaseEnvironment)); err != nil {
		return 124
	}
	root := os.Getenv(realZoxideRootEnvironment)
	if root == "" {
		return 4
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\n%s\n", filepath.Join(root, "late-match-target"), filepath.Join(root, "other-target")); err != nil {
		return 5
	}
	return 0
}
