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
	startedChild := false
	if category == CategoryBinary {
		category, startedChild = fileHint(renderCtx, path, category, options, budget, stderr)
	}
	if category == CategoryZip {
		if preflightErr := preflightZip(path, limits.MaxArchiveEntries); preflightErr != nil {
			if !startedChild && options.OnDispatch != nil {
				options.OnDispatch("native", 0, 0)
			}
			return renderNative(renderCtx, path, info, prefix, category, stdout, limits)
		}
	}
	startedChild, rendered := renderExternal(renderCtx, path, category, options, stdout, stderr, startedChild)
	_, exceeded := budget.status()
	if exceeded {
		return ErrOutputLimit
	}
	if err := renderCtx.Err(); err != nil {
		return err
	}
	if rendered {
		return nil
	}
	if !startedChild && options.OnDispatch != nil {
		options.OnDispatch("native", 0, 0)
	}
	err = renderNative(renderCtx, path, info, prefix, category, stdout, limits)
	return err
}

func renderExternal(ctx context.Context, path string, category Category, options Options, stdout *budgetWriter, stderr io.Writer, started bool) (bool, bool) {
	for _, tool := range externalTools(category, path, options) {
		executable := lookupTool(tool.name, options.Environment)
		if executable == "" {
			continue
		}
		before := stdout.bytesWritten()
		child, err := options.Runner.Start(ctx, externalProcessSpec(executable, tool.arguments, options.Environment, stdout, stderr))
		if err != nil {
			continue
		}
		if !started && options.OnDispatch != nil {
			options.OnDispatch(tool.name, child.PID(), 0)
		}
		started = true
		err = child.Wait()
		if ctx.Err() != nil {
			return started, false
		}
		if err == nil && stdout.bytesWritten() > before {
			return started, true
		}
	}
	return started, false
}

type directTool struct {
	name      string
	arguments []string
}

func externalTools(category Category, path string, options Options) []directTool {
	switch category {
	case CategoryDirectory:
		return []directTool{{"eza", []string{"--color=always", "--icons=always", "--group-directories-first", "--", path}}}
	case CategoryMarkdown:
		return []directTool{{"glow", []string{"--width", strconv.Itoa(max(1, options.Columns-1)), path}},
			{"bat", []string{"--color=always", "--style=plain", "--paging=never", "--", path}}}
	case CategoryText:
		return []directTool{{"bat", []string{"--color=always", "--style=plain", "--paging=never", "--", path}}}
	case CategoryImage:
		tools := []directTool{}
		if terminalQualified(options.Environment) {
			tools = append(tools, directTool{"kitten", []string{"icat", "--clear", "--transfer-mode=memory", "--place",
				fmt.Sprintf("%dx%d@0x0", options.Columns, options.Lines), "--", path}})
		}
		return append(tools, directTool{"chafa", []string{"--size", fmt.Sprintf("%dx%d", options.Columns, options.Lines), "--", path}})
	case CategoryAudio:
		return []directTool{{"exiftool", []string{"--", path}}}
	case CategoryZip:
		return []directTool{{"unzip", []string{"-l", "--", path}}}
	case CategoryGzip:
		return []directTool{{"gzip", []string{"--list", "--", path}}}
	case CategoryXz:
		return []directTool{{"xz", []string{"--list", "--", path}}}
	case CategoryTar:
		return []directTool{{"tar", []string{"--list", "--file", path}}}
	case CategoryBzip:
		return []directTool{{"bzip2", []string{"--test", "--verbose", "--", path}}}
	default:
		return nil
	}
}

func terminalQualified(environment []string) bool {
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "TERM" && strings.Contains(strings.ToLower(value), "kitty") {
			return true
		}
	}
	return false
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
