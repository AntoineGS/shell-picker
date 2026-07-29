package preview

import (
	"bufio"
	"context"
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
	Cache          *Cache
	retainTree     func(*process.Child) (treeHandle, error)
}

func Render(ctx context.Context, candidate protocol.ResolvedCandidate, options Options) error {
	if candidate.Kind == protocol.KindVirtual || !filepath.IsAbs(string(candidate.Path)) {
		return ErrPathNotAbsolute
	}
	limits := normalizedLimits(options.Limits)
	options.Limits = limits
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
	session := newRenderSession(options)
	defer session.close()
	if category == CategoryBinary {
		category, err = fileHint(renderCtx, path, category, options, budget, stderr, session)
		if err != nil {
			return err
		}
	}
	if !info.IsDir() && info.Size() > limits.MaxArtifactBytes && !archiveCategory(category) {
		return ErrArtifactLimit
	}
	nativeOnly := category == CategoryZip && preflightZip(path, limits.MaxArchiveEntries) != nil
	if nativeOnly && !session.started && options.OnDispatch != nil {
		options.OnDispatch("native", 0, 0)
	}
	rendered, richHandled := false, false
	if !nativeOnly {
		rendered, richHandled, err = renderCachedArtifact(renderCtx, candidate, category, options, stdout, stderr, session)
		if err == nil && !rendered && !richHandled {
			rendered, err = renderExternal(renderCtx, path, category, options, stdout, stderr, session)
		}
		if err != nil {
			return err
		}
	}
	if resourceErr := resourceFailure(renderCtx, budget, nil); resourceErr != nil {
		if session.started {
			return session.terminal(resourceErr)
		}
		return resourceErr
	}
	if rendered {
		return nil
	}
	if !session.started && options.OnDispatch != nil {
		options.OnDispatch("native", 0, 0)
	}
	err = renderNative(renderCtx, path, info, prefix, category, stdout, limits)
	if session.started {
		if resourceErr := resourceFailure(renderCtx, budget, err); resourceErr != nil {
			return session.terminal(resourceErr)
		}
	}
	return err
}
func renderExternal(ctx context.Context, path string, category Category, options Options, stdout *budgetWriter,
	stderr io.Writer, session *renderSession, extraFiles ...*os.File) (bool, error) {
	for _, tool := range externalTools(category, path, options) {
		executable := lookupTool(tool.name, options.Environment)
		if executable == "" {
			continue
		}
		before, processStdout := stdout.meaningfulBytes(), io.Writer(stdout)
		if category == CategoryZip || category == CategoryGzip || category == CategoryXz || category == CategoryTar || category == CategoryBzip {
			processStdout = &lineLimitWriter{writer: stdout, remaining: options.Limits.MaxArchiveEntries}
		}
		spec := externalProcessSpec(executable, tool.arguments, options.Environment, processStdout, stderr)
		for _, file := range extraFiles {
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return false, session.terminal(err)
			}
		}
		spec.ExtraFiles = extraFiles
		child, err := options.Runner.Start(ctx, spec)
		if err != nil {
			continue
		}
		first, retainErr := session.start(child)
		if first && options.OnDispatch != nil {
			options.OnDispatch(tool.name, child.PID(), 0)
		}
		err = child.Wait()
		if retainErr != nil {
			return false, session.terminal(retainErr)
		}
		if resourceErr := resourceFailure(ctx, stdout.budget, err); resourceErr != nil {
			return false, session.terminal(resourceErr)
		}
		if stdout.meaningfulBytes() > before {
			return true, nil
		}
	}
	return false, nil
}

type directTool struct {
	name      string
	arguments []string
}

func richConverterArguments(category Category, path, artifact string) []string {
	switch category {
	case CategoryPDF:
		return []string{"-singlefile", "-jpeg", path, strings.TrimSuffix(artifact, ".jpg")}
	case CategoryVideo:
		return []string{"-i", path, "-o", artifact, "-s", "1080", "-m"}
	default:
		return []string{"-y", "-i", path, "-an", "-c:v", "copy", artifact}
	}
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
		return []directTool{{"gzip", []string{"-l", "--", path}}}
	case CategoryXz:
		return []directTool{{"xz", []string{"-l", "--", path}}}
	case CategoryTar:
		return []directTool{{"tar", []string{"tf", path}}}
	case CategoryBzip:
		return []directTool{{"tar", []string{"tf", path}}}
	default:
		return nil
	}
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
	prefix = prefix[:min(len(prefix), 4<<10)]
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
