package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
)

func writeHashUint64(writer io.Writer, value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	_, _ = writer.Write(data[:])
}

func terminalQualified(environment []string) bool {
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "TERM" && strings.Contains(strings.ToLower(value), "kitty") {
			return true
		}
	}
	return false
}

var (
	ErrOutputLimit      = errors.New("preview: output limit exceeded")
	ErrInputLimit       = errors.New("preview: input limit exceeded")
	ErrArtifactLimit    = errors.New("preview: artifact limit exceeded")
	ErrArchiveEntries   = errors.New("preview: archive entry limit exceeded")
	ErrPathNotAbsolute  = errors.New("preview: path is not an absolute filesystem candidate")
	ErrTerminalResource = errors.New("preview: terminal resource failure")
)

type Limits struct {
	Deadline                    time.Duration
	MaxOutputBytes              int64
	MaxInternalInputBytes       int64
	MaxInternalLines            int
	MaxArchiveEntries           int
	MaxArchiveDecompressedBytes int64
	MaxArtifactBytes            int64
}

type treeHandle interface {
	KillTree() error
	Close() error
}

type renderSession struct {
	tree       treeHandle
	retainTree func(*process.Child) (treeHandle, error)
	started    bool
	cleanup    func()
}

func newRenderSession(options Options) *renderSession {
	retain := options.retainTree
	if retain == nil {
		retain = func(child *process.Child) (treeHandle, error) { return child.RetainTree() }
	}
	return &renderSession{retainTree: retain}
}

func (session *renderSession) start(child *process.Child) (bool, error) {
	first := !session.started
	session.started = true
	if !first {
		return false, nil
	}
	tree, err := session.retainTree(child)
	if err == nil {
		session.tree = tree
	}
	return true, err
}

func (session *renderSession) terminal(cause error) error {
	if session.cleanup != nil {
		session.cleanup()
		session.cleanup = nil
	}
	var killErr error
	if session.tree != nil {
		killErr = session.tree.KillTree()
	}
	return errors.Join(ErrTerminalResource, cause, killErr)
}

func (session *renderSession) close() {
	if session.tree != nil {
		_ = session.tree.Close()
	}
}

func resourceFailure(ctx context.Context, budget *outputBudget, err error) error {
	if _, exceeded := budget.status(); exceeded {
		return ErrOutputLimit
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, process.ErrWaitDelay) || errors.Is(err, ErrOutputLimit) {
		return err
	}
	return nil
}

var DefaultLimits = Limits{
	Deadline: 10 * time.Second, MaxOutputBytes: 4 << 20, MaxInternalInputBytes: 4 << 20,
	MaxInternalLines: 10_000, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 4 << 20,
	MaxArtifactBytes: 64 << 20,
}

func normalizedLimits(limits Limits) Limits {
	if limits.Deadline <= 0 {
		limits.Deadline = DefaultLimits.Deadline
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultLimits.MaxOutputBytes
	}
	if limits.MaxInternalInputBytes <= 0 {
		limits.MaxInternalInputBytes = DefaultLimits.MaxInternalInputBytes
	}
	if limits.MaxInternalLines <= 0 {
		limits.MaxInternalLines = DefaultLimits.MaxInternalLines
	}
	if limits.MaxArchiveEntries <= 0 {
		limits.MaxArchiveEntries = DefaultLimits.MaxArchiveEntries
	}
	if limits.MaxArchiveDecompressedBytes <= 0 {
		limits.MaxArchiveDecompressedBytes = DefaultLimits.MaxArchiveDecompressedBytes
	}
	if limits.MaxArtifactBytes <= 0 {
		limits.MaxArtifactBytes = DefaultLimits.MaxArtifactBytes
	}
	return limits
}

type countingWriter struct {
	writer *budgetWriter
}

func newCountingWriter(destination io.Writer, maximum int64) *countingWriter {
	budget := newOutputBudget(maximum, nil)
	return &countingWriter{writer: budget.writer(destination)}
}

func (writer *countingWriter) Write(data []byte) (int, error) {
	return writer.writer.Write(data)
}

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
	written   int64
	exceeded  bool
	onLimit   func()
}

type budgetWriter struct {
	budget      *outputBudget
	destination io.Writer
	written     int64
	meaningful  int64
}

func newOutputBudget(maximum int64, onLimit func()) *outputBudget {
	return &outputBudget{remaining: maximum, onLimit: onLimit}
}

func (budget *outputBudget) writer(destination io.Writer) *budgetWriter {
	if destination == nil {
		destination = io.Discard
	}
	return &budgetWriter{budget: budget, destination: destination}
}

func (writer *budgetWriter) Write(data []byte) (int, error) {
	budget := writer.budget
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.remaining <= 0 {
		budget.limitReachedLocked()
		return 0, ErrOutputLimit
	}
	allowed := len(data)
	limited := int64(allowed) > budget.remaining
	if limited {
		allowed = int(budget.remaining)
	}
	written, err := writer.destination.Write(data[:allowed])
	budget.remaining -= int64(written)
	budget.written += int64(written)
	writer.written += int64(written)
	for _, value := range data[:written] {
		if value != ' ' && value != '\t' && value != '\n' && value != '\r' && value != '\v' && value != '\f' {
			writer.meaningful++
		}
	}
	if err != nil {
		return written, err
	}
	if limited && written == allowed {
		budget.limitReachedLocked()
		return written, ErrOutputLimit
	}
	return written, nil
}

func (writer *budgetWriter) bytesWritten() int64 {
	writer.budget.mu.Lock()
	defer writer.budget.mu.Unlock()
	return writer.written
}

func (writer *budgetWriter) meaningfulBytes() int64 {
	writer.budget.mu.Lock()
	defer writer.budget.mu.Unlock()
	return writer.meaningful
}

func (budget *outputBudget) limitReachedLocked() {
	if budget.exceeded {
		return
	}
	budget.exceeded = true
	if budget.onLimit != nil {
		budget.onLimit()
	}
}

func (budget *outputBudget) status() (int64, bool) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.written, budget.exceeded
}

func externalProcessSpec(executable string, arguments, environment []string, stdout, stderr io.Writer) process.Spec {
	return process.Spec{Path: executable, Args: arguments, Env: environment, Stdout: stdout, Stderr: stderr,
		Containment: process.ContainmentInheritTree, WaitDelay: time.Second}
}

func lookupTool(name string, environment []string) string {
	search := environmentValue(environment, "PATH")
	if search == "" {
		return ""
	}
	extensions := []string{""}
	if runtime.GOOS == "windows" {
		extensions = []string{".exe", ".com", ".bat", ".cmd"}
	}
	for _, directory := range filepath.SplitList(search) {
		for _, extension := range extensions {
			candidate := filepath.Join(directory, name+extension)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
				if absolute, absoluteErr := filepath.Abs(candidate); absoluteErr == nil {
					return absolute
				}
			}
		}
	}
	return ""
}

func environmentValue(environment []string, name string) string {
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return value
		}
	}
	return ""
}

func fileHint(ctx context.Context, path string, fallback Category, options Options, budget *outputBudget,
	stderr io.Writer, session *renderSession) (Category, error) {
	executable := lookupTool("file", options.Environment)
	if executable == "" {
		return fallback, nil
	}
	var hint bytes.Buffer
	child, err := options.Runner.Start(ctx, externalProcessSpec(executable,
		[]string{"--brief", "--mime-type", "--", path}, options.Environment, budget.writer(&hint), stderr))
	if err != nil {
		return fallback, nil
	}
	first, retainErr := session.start(child)
	if first && options.OnDispatch != nil {
		options.OnDispatch("file", child.PID(), 0)
	}
	waitErr := child.Wait()
	if retainErr != nil {
		return fallback, session.terminal(retainErr)
	}
	if resourceErr := resourceFailure(ctx, budget, waitErr); resourceErr != nil {
		return fallback, session.terminal(resourceErr)
	}
	if waitErr != nil {
		return fallback, nil
	}
	return categoryFromMIME(strings.TrimSpace(hint.String()), fallback), nil
}

func categoryFromMIME(mime string, fallback Category) Category {
	switch {
	case mime == "text/markdown":
		return CategoryMarkdown
	case strings.HasPrefix(mime, "text/"):
		return CategoryText
	case strings.HasPrefix(mime, "image/"):
		return CategoryImage
	case mime == "application/pdf":
		return CategoryPDF
	case strings.HasPrefix(mime, "video/"):
		return CategoryVideo
	case strings.HasPrefix(mime, "audio/"):
		return CategoryAudio
	case mime == "application/zip":
		return CategoryZip
	case mime == "application/gzip" || mime == "application/x-gzip":
		return CategoryGzip
	case mime == "application/x-xz":
		return CategoryXz
	case mime == "application/x-tar":
		return CategoryTar
	case mime == "application/x-bzip2":
		return CategoryBzip
	default:
		return fallback
	}
}
