package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/integration/windowsnative"
)

type goCommandRunner func(args []string, environment []string, stdout, stderr io.Writer) error

const (
	maxListDiagnosticBytes = 4096
	listDiagnosticMarker   = "[...truncated...]"
)

type boundedCapture struct {
	limit     int
	head      []byte
	tail      []byte
	tailStart int
	tailCount int
	total     uint64
	overflow  bool
}

func newBoundedCapture(limit int) *boundedCapture {
	if limit < 0 {
		limit = 0
	}
	headLimit := limit / 2
	return &boundedCapture{
		limit: limit,
		head:  make([]byte, 0, headLimit),
		tail:  make([]byte, limit-headLimit),
	}
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	n := len(p)
	c.total += uint64(n)
	if len(c.head) < cap(c.head) {
		headBytes := min(len(p), cap(c.head)-len(c.head))
		c.head = append(c.head, p[:headBytes]...)
		p = p[headBytes:]
	}
	if len(p) > 0 {
		c.appendTail(p)
	}
	if c.total > uint64(c.limit) {
		c.overflow = true
	}
	return n, nil
}

func (c *boundedCapture) appendTail(p []byte) {
	if len(c.tail) == 0 {
		return
	}
	if len(p) >= len(c.tail) {
		copy(c.tail, p[len(p)-len(c.tail):])
		c.tailStart = 0
		c.tailCount = len(c.tail)
		return
	}
	for _, b := range p {
		if c.tailCount < len(c.tail) {
			c.tail[(c.tailStart+c.tailCount)%len(c.tail)] = b
			c.tailCount++
			continue
		}
		c.tail[c.tailStart] = b
		c.tailStart = (c.tailStart + 1) % len(c.tail)
	}
}

func (c *boundedCapture) retainedLen() int {
	return cap(c.head) + len(c.tail)
}

func (c *boundedCapture) tailBytes() []byte {
	if c.tailCount == 0 {
		return nil
	}
	if c.tailStart+c.tailCount <= len(c.tail) {
		return c.tail[c.tailStart : c.tailStart+c.tailCount]
	}
	result := make([]byte, c.tailCount)
	n := copy(result, c.tail[c.tailStart:])
	copy(result[n:], c.tail[:c.tailCount-n])
	return result
}

func (c *boundedCapture) diagnostic() string {
	tail := c.tailBytes()
	if !c.overflow {
		return string(c.head) + string(tail)
	}
	if c.limit <= len(listDiagnosticMarker) {
		return listDiagnosticMarker[:c.limit]
	}
	dataLimit := c.limit - len(listDiagnosticMarker)
	headLimit := dataLimit / 2
	tailLimit := dataLimit - headLimit
	head := c.head
	if len(head) > headLimit {
		head = head[:headLimit]
	}
	if len(tail) > tailLimit {
		tail = tail[len(tail)-tailLimit:]
	}
	return string(head) + listDiagnosticMarker + string(tail)
}

func formatListCapture(label string, capture *boundedCapture) string {
	return fmt.Sprintf("%s: total=%d overflow=%t content=%s", label, capture.total, capture.overflow, capture.diagnostic())
}

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

func executeGoCommand(args []string, environment []string, stdout, stderr io.Writer) error {
	command := exec.Command("go", args...)
	command.Env = environment
	command.Stdout, command.Stderr = stdout, stderr
	return command.Run()
}

func runManifest(packages []windowsnative.Package, environment []string, stdout, stderr io.Writer, runCommand goCommandRunner) error {
	for _, pkg := range packages {
		if err := validatePackageList(pkg, environment, runCommand); err != nil {
			return fmt.Errorf("%s: %w", pkg.Path, err)
		}

		args := []string{"test", pkg.Path, "-run", pkg.Pattern, "-count=1", "-timeout=5m"}
		if err := runCommand(args, environment, stdout, stderr); err != nil {
			return fmt.Errorf("%s: %w", pkg.Path, err)
		}
	}
	return nil
}

func validatePackageList(pkg windowsnative.Package, environment []string, runCommand goCommandRunner) error {
	args := []string{"test", pkg.Path, "-list", pkg.Pattern, "-count=1", "-timeout=5m"}
	listOutput := newBoundedCapture(maxListDiagnosticBytes)
	listError := newBoundedCapture(maxListDiagnosticBytes)
	if err := runCommand(args, environment, listOutput, listError); err != nil {
		return fmt.Errorf("list command failed: %w; %s; %s", err,
			formatListCapture("list stdout", listOutput), formatListCapture("list stderr", listError))
	}
	if listOutput.overflow {
		return fmt.Errorf("list stdout exceeded capture limit: %s", formatListCapture("list stdout", listOutput))
	}
	if listError.overflow {
		return fmt.Errorf("list stderr exceeded capture limit: %s", formatListCapture("list stderr", listError))
	}
	if detail := strings.TrimSpace(listError.diagnostic()); detail != "" {
		return fmt.Errorf("list command wrote unexpected stderr: %s", formatListCapture("list stderr", listError))
	}
	return validateListOutput(pkg.Pattern, listOutput.diagnostic())
}

var goTestDuration = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?s$`)
var literalTestName = regexp.MustCompile(`^Test[A-Z][A-Za-z0-9_]*$`)

func validateListOutput(pattern, output string) error {
	expected, err := exactPatternNames(pattern)
	if err != nil {
		return err
	}
	expectedSet := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		expectedSet[name] = struct{}{}
	}

	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return fmt.Errorf("list output is empty")
	}

	counts := make(map[string]int, len(expected))
	statusSeen := false
	for index, line := range lines {
		if line == "" {
			return fmt.Errorf("list output contains an empty line")
		}
		if isGoTestStatusLine(line) {
			if statusSeen || index != len(lines)-1 {
				return fmt.Errorf("list output contains a misplaced status line")
			}
			statusSeen = true
			continue
		}
		if statusSeen {
			return fmt.Errorf("list output contains data after the status line")
		}
		if !literalTestName.MatchString(line) {
			return fmt.Errorf("malformed list output line %q", line)
		}
		if _, ok := expectedSet[line]; !ok {
			return fmt.Errorf("unexpected test name %q", line)
		}
		counts[line]++
		if counts[line] > 1 {
			return fmt.Errorf("duplicate test name %q", line)
		}
	}
	if !statusSeen {
		return fmt.Errorf("list output is missing the go test status line")
	}
	for _, name := range expected {
		if counts[name] != 1 {
			return fmt.Errorf("missing test name %q", name)
		}
	}
	return nil
}

func isGoTestStatusLine(line string) bool {
	fields := strings.Fields(line)
	return len(fields) == 3 && fields[0] == "ok" &&
		(fields[2] == "(cached)" || goTestDuration.MatchString(fields[2]))
}

func exactPatternNames(pattern string) ([]string, error) {
	const (
		prefix = "^("
		suffix = ")$"
	)
	if !strings.HasPrefix(pattern, prefix) || !strings.HasSuffix(pattern, suffix) {
		return nil, fmt.Errorf("manifest pattern is not exactly anchored: %q", pattern)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(pattern, prefix), suffix)
	if body == "" {
		return nil, fmt.Errorf("manifest pattern has no test names")
	}
	names := strings.Split(body, "|")
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !literalTestName.MatchString(name) {
			return nil, fmt.Errorf("manifest pattern contains malformed test name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("manifest pattern contains duplicate test name %q", name)
		}
		seen[name] = struct{}{}
	}
	return names, nil
}

func run(arguments, environment []string, goos string, stdout, stderr io.Writer, runCommand goCommandRunner) int {
	if len(arguments) != 0 {
		fmt.Fprintln(stderr, "windowsnative accepts no arguments")
		return 2
	}
	if goos != "windows" {
		fmt.Fprintln(stderr, "windowsnative requires GOOS=windows at runtime")
		return 2
	}
	if err := windowsnative.Validate(); err != nil {
		fmt.Fprintf(stderr, "invalid Windows manifest: %v\n", err)
		return 2
	}

	controlledEnvironment := append(filteredGoEnvironment(environment), "GOENV=off", "GOFLAGS=")
	if err := runManifest(windowsnative.Packages, controlledEnvironment, stdout, stderr, runCommand); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), runtime.GOOS, os.Stdout, os.Stderr, executeGoCommand))
}
