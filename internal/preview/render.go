package preview

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type Options struct {
	Columns, Lines int
	Environment    []string
	Runner         process.Runner
	Limits         Limits
	Stdout, Stderr io.Writer
	OnDispatch     func(string, int, time.Duration)
}

func Render(ctx context.Context, candidate protocol.ResolvedCandidate, options Options) error {
	if candidate.Kind == protocol.KindVirtual || !filepath.IsAbs(string(candidate.Path)) {
		return ErrPathNotAbsolute
	}
	limits := normalizedLimits(options.Limits)
	renderCtx, cancel := context.WithTimeout(ctx, limits.Deadline)
	defer cancel()
	if err := renderCtx.Err(); err != nil {
		return err
	}
	path := string(candidate.Path)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() && info.Size() > limits.MaxArtifactBytes {
		return ErrArtifactLimit
	}
	prefix, err := readPrefix(renderCtx, path, info, 64<<10)
	if err != nil {
		return err
	}
	category, err := Detect(prefix, info)
	if err != nil {
		return err
	}
	budget := newOutputBudget(limits.MaxOutputBytes, cancel)
	stdout, stderr := budget.writer(options.Stdout), budget.writer(options.Stderr)
	started := time.Now()
	used, externalErr := renderExternal(renderCtx, path, category, options, stdout, stderr)
	_, exceeded := budget.status()
	if exceeded {
		return ErrOutputLimit
	}
	if err := renderCtx.Err(); err != nil {
		return err
	}
	if used && externalErr == nil && stdout.bytesWritten() > 0 {
		return nil
	}
	if options.OnDispatch != nil {
		options.OnDispatch("native", 0, 0)
	}
	err = renderNative(renderCtx, path, info, prefix, category, stdout, limits)
	if options.OnDispatch != nil {
		options.OnDispatch("native", 0, time.Since(started))
	}
	return err
}

func renderExternal(ctx context.Context, path string, category Category, options Options, stdout, stderr io.Writer) (bool, error) {
	tool, arguments := externalTool(category, path)
	if tool == "" {
		return false, nil
	}
	executable := lookupTool(tool, options.Environment)
	if executable == "" {
		return false, nil
	}
	started := time.Now()
	child, err := options.Runner.Start(ctx, externalProcessSpec(executable, arguments, options.Environment, stdout, stderr))
	if err != nil {
		return false, err
	}
	if options.OnDispatch != nil {
		options.OnDispatch(tool, child.PID(), 0)
	}
	err = child.Wait()
	if options.OnDispatch != nil {
		options.OnDispatch(tool, child.PID(), time.Since(started))
	}
	return true, err
}

func externalProcessSpec(executable string, arguments, environment []string, stdout, stderr io.Writer) process.Spec {
	return process.Spec{Path: executable, Args: arguments, Env: environment, Stdout: stdout, Stderr: stderr,
		Containment: process.ContainmentInheritTree, WaitDelay: time.Second}
}

func externalTool(category Category, path string) (string, []string) {
	switch category {
	case CategoryMarkdown, CategoryText:
		return "bat", []string{"--color=always", "--style=plain", "--paging=never", "--", path}
	case CategoryImage:
		return "chafa", []string{"--", path}
	case CategoryPDF:
		return "pdftotext", []string{path, "-"}
	case CategoryVideo, CategoryAudio:
		return "ffprobe", []string{"-hide_banner", path}
	default:
		return "", nil
	}
}

func lookupTool(name string, environment []string) string {
	var search string
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && ((runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) || key == "PATH") {
			search = value
		}
	}
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
				absolute, err := filepath.Abs(candidate)
				if err == nil {
					return absolute
				}
			}
		}
	}
	return ""
}

func readPrefix(ctx context.Context, path string, info os.FileInfo, maximum int64) ([]byte, error) {
	if info.IsDir() {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maximum))
}

func renderNative(ctx context.Context, path string, info os.FileInfo, prefix []byte, category Category,
	output io.Writer, limits Limits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch category {
	case CategoryDirectory:
		return renderDirectory(ctx, path, output, limits)
	case CategoryMarkdown, CategoryText:
		return renderText(ctx, path, output, limits)
	case CategoryImage:
		return renderImage(path, info, output)
	case CategoryPDF:
		return renderPDF(info, prefix, output)
	case CategoryVideo, CategoryAudio:
		return renderMetadata(category, info, output)
	case CategoryZip:
		return renderArchiveOrFallback(ctx, CategoryZip, info, output, func() error {
			return renderZip(ctx, path, output, limits)
		})
	case CategoryGzip:
		return renderArchiveOrFallback(ctx, CategoryGzip, info, output, func() error {
			return renderGzip(ctx, path, output, limits)
		})
	case CategoryTar:
		return renderArchiveOrFallback(ctx, CategoryTar, info, output, func() error {
			return renderTar(ctx, path, output, limits)
		})
	case CategoryXz, CategoryBzip, CategoryBinary:
		return renderMetadata(category, info, output)
	default:
		return fmt.Errorf("preview: unsupported category %q", category)
	}
}

func renderArchiveOrFallback(ctx context.Context, category Category, info os.FileInfo, output io.Writer, render func() error) error {
	err := render()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, ErrOutputLimit) || errors.Is(err, ErrInputLimit) || errors.Is(err, ErrArchiveEntries) {
		return err
	}
	return renderMetadata(category, info, output)
}

func renderDirectory(ctx context.Context, path string, output io.Writer, limits Limits) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(limits.MaxInternalLines)
	if err != nil && err != io.EOF {
		return err
	}
	if _, err := fmt.Fprintln(output, "Directory:"); err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(output, escaped(name)); err != nil {
			return err
		}
	}
	if len(names) == 0 {
		_, err = fmt.Fprintln(output, "(empty)")
	}
	return err
}

func renderText(ctx context.Context, path string, output io.Writer, limits Limits) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, limits.MaxInternalInputBytes))
	maximumToken := limits.MaxInternalInputBytes
	maximumInt := int64(^uint(0) >> 1)
	if maximumToken < maximumInt {
		maximumToken++
	} else {
		maximumToken = maximumInt
	}
	scanner.Buffer(make([]byte, min(maximumToken, int64(64<<10))), int(maximumToken))
	lines := 0
	for scanner.Scan() && lines < limits.MaxInternalLines {
		if err := ctx.Err(); err != nil {
			return err
		}
		lines++
		line := scanner.Text()
		const maximumRenderedLineBytes = 64 << 10
		truncated := ""
		if len(line) > maximumRenderedLineBytes {
			line = line[:maximumRenderedLineBytes]
			truncated = " ... [truncated]"
		}
		if _, err := fmt.Fprintf(output, "%6d  %s%s\n", lines, escaped(line), truncated); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if lines == 0 {
		_, err = fmt.Fprintln(output, "(empty text file)")
	}
	return err
}

func renderImage(path string, info os.FileInfo, output io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(io.LimitReader(file, 64<<10))
	if err != nil {
		return renderMetadata(CategoryImage, info, output)
	}
	_, err = fmt.Fprintf(output, "Image: %s, %dx%d, %d bytes, modified %s\n", format, config.Width, config.Height,
		info.Size(), info.ModTime().Format(time.RFC3339))
	return err
}

func renderPDF(info os.FileInfo, prefix []byte, output io.Writer) error {
	if _, err := fmt.Fprintf(output, "PDF document: %d bytes, modified %s\n", info.Size(), info.ModTime().Format(time.RFC3339)); err != nil {
		return err
	}
	var printable strings.Builder
	for _, value := range prefix {
		if value == '\n' || value == '\r' || value == '\t' || unicode.IsPrint(rune(value)) {
			printable.WriteByte(value)
		} else {
			printable.WriteByte(' ')
		}
	}
	_, err := fmt.Fprintln(output, escaped(printable.String()))
	return err
}

func renderMetadata(category Category, info os.FileInfo, output io.Writer) error {
	_, err := fmt.Fprintf(output, "%s file: %d bytes, modified %s\n", category, info.Size(), info.ModTime().Format(time.RFC3339))
	return err
}

func escaped(value string) string {
	quoted := strconv.QuoteToASCII(value)
	return quoted[1 : len(quoted)-1]
}
