package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
)

var defaultPreviewTools = []string{"bat", "chafa", "eza", "exiftool", "ffmpeg", "ffmpegthumbnailer", "file", "glow", "gzip", "kitten", "pdftoppm", "tar", "unzip", "xz"}

type ProbeDependency struct {
	Status  string `json:"status"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

type ProbeFZF struct {
	ProbeDependency
}

type ProbeZoxide struct {
	ProbeDependency
	DefaultPolicy  string `json:"default_policy"`
	ExactParity    string `json:"exact_parity"`
	DefaultTimeout string `json:"default_timeout"`
}

type ProbeCache struct {
	Root   string `json:"root,omitempty"`
	Status string `json:"status"`
}

type ProbeTerminal struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

type ProbeAdapter struct {
	Zsh     string `json:"zsh"`
	Nushell string `json:"nushell"`
}

type ProbeWindows struct {
	Job    string `json:"job"`
	ConPTY string `json:"conpty"`
}

type ProbeReport struct {
	Schema             int                        `json:"schema"`
	Ready              bool                       `json:"ready"`
	OS                 string                     `json:"os"`
	Arch               string                     `json:"arch"`
	ApplicationVersion string                     `json:"application_version"`
	FZF                ProbeFZF                   `json:"fzf"`
	Zoxide             ProbeZoxide                `json:"zoxide"`
	Tools              map[string]ProbeDependency `json:"tools"`
	Cache              ProbeCache                 `json:"cache"`
	Terminal           ProbeTerminal              `json:"terminal"`
	Adapter            ProbeAdapter               `json:"adapter"`
	Windows            ProbeWindows               `json:"windows"`
}

type ProbeOptions struct {
	Version              string
	FZFPath              string
	ZoxidePath           string
	PreviewTools         []string
	Environment          []string
	Runner               process.Runner
	LookupPath           func(string) (string, error)
	CheckFZF             func(context.Context, string, []string) (string, error)
	Cache                ProbeCache
	Terminal             ProbeTerminal
	Adapter              ProbeAdapter
	Windows              ProbeWindows
	DefaultZoxideTimeout time.Duration
}

func Probe(ctx context.Context, options ProbeOptions) ProbeReport {
	report := ProbeReport{Schema: 1, OS: runtime.GOOS, Arch: runtime.GOARCH, ApplicationVersion: options.Version,
		Tools: make(map[string]ProbeDependency)}
	lookup := options.LookupPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	environment := process.SanitizeEnv(options.Environment, nil)
	if options.Environment == nil {
		environment = process.SanitizeEnv(os.Environ(), nil)
	}
	fzfPath := options.FZFPath
	if fzfPath == "" {
		fzfPath = "fzf"
	}
	resolvedFZF, lookupErr := lookup(fzfPath)
	if lookupErr != nil {
		report.FZF.Status = "missing"
	} else {
		report.FZF.Path = redact([]byte(resolvedFZF))
		check := options.CheckFZF
		if check == nil {
			check = func(checkCtx context.Context, path string, env []string) (string, error) {
				checkErr := fzf.CheckVersion(checkCtx, options.Runner, path)
				var output bytes.Buffer
				err := options.Runner.Run(checkCtx, process.Spec{Path: path, Args: []string{"--version"}, Env: env,
					Stdout: &output, Containment: process.ContainmentOwnTree, WaitDelay: time.Second})
				fields := strings.Fields(output.String())
				if err != nil || len(fields) == 0 {
					return "", errors.Join(checkErr, errors.New("probe: fzf version unavailable"))
				}
				return fields[0], checkErr
			}
		}
		version, checkErr := check(ctx, resolvedFZF, environment)
		report.FZF.Version = boundedVersion(version)
		switch {
		case checkErr == nil:
			report.FZF.Status = "ready"
			report.Ready = true
		case version != "":
			report.FZF.Status = "version-unsupported"
		default:
			report.FZF.Status = "unavailable"
		}
	}

	timeout := options.DefaultZoxideTimeout
	if timeout == 0 {
		timeout = candidate.DefaultZoxideTimeout()
	}
	report.Zoxide.DefaultPolicy = "cached"
	report.Zoxide.ExactParity = "--zoxide-policy fresh --zoxide-timeout 0"
	report.Zoxide.DefaultTimeout = timeout.String()
	zoxidePath := options.ZoxidePath
	if zoxidePath == "" {
		zoxidePath = "zoxide"
	}
	if resolved, err := lookup(zoxidePath); err == nil {
		report.Zoxide.Status = "available"
		report.Zoxide.Path = redact([]byte(resolved))
	} else {
		report.Zoxide.Status = "optional-missing"
	}
	tools := options.PreviewTools
	if tools == nil {
		tools = defaultPreviewTools
	}
	for _, tool := range tools {
		if !validToolName(tool) {
			continue
		}
		dependency := ProbeDependency{Status: "optional-missing"}
		if path, err := lookup(tool); err == nil {
			dependency.Status, dependency.Path = "available", redact([]byte(path))
		}
		report.Tools[tool] = dependency
	}
	report.Cache = normalizedCache(options.Cache, environment)
	report.Terminal = normalizedTerminal(options.Terminal)
	report.Adapter = normalizedAdapter(options.Adapter, environment)
	report.Windows = normalizedWindows(options.Windows)
	return report
}

func normalizedCache(cache ProbeCache, environment []string) ProbeCache {
	if cache.Root == "" {
		cache.Root = defaultProbeCacheRoot(environment)
		if cache.Status == "" {
			cache.Status = cacheStatus(cache.Root)
		}
	}
	if cache.Root != "" {
		cache.Root = redact([]byte(cache.Root))
	}
	if cache.Status != "writable" && cache.Status != "read-only" && cache.Status != "missing" && cache.Status != "unavailable" {
		cache.Status = "unknown"
	}
	return cache
}

func defaultProbeCacheRoot(environment []string) string {
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[strings.ToUpper(key)] = value
		}
	}
	home := ""
	if resolved, err := os.UserHomeDir(); err == nil {
		home = resolved
	}
	return probeCacheRoot(runtime.GOOS, home, values)
}

func probeCacheRoot(goos, home string, values map[string]string) string {
	base := values["XDG_CACHE_HOME"]
	if goos == "windows" {
		base = values["LOCALAPPDATA"]
	}
	if base == "" && home != "" {
		suffix := ".cache"
		if goos == "windows" {
			suffix = filepath.Join("AppData", "Local")
		}
		base = filepath.Join(home, suffix)
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "shell-picker", "previews")
}

func cacheStatus(root string) string {
	if root == "" {
		return "unavailable"
	}
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil || !info.IsDir() {
		return "unavailable"
	}
	file, err := os.CreateTemp(root, ".probe-")
	if err != nil {
		return "read-only"
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return "writable"
}

func normalizedTerminal(value ProbeTerminal) ProbeTerminal {
	if value.Input == "" {
		value.Input = terminalStatus(os.Stdin)
	}
	if value.Output == "" {
		value.Output = terminalStatus(os.Stdout)
	}
	value.Input = boundedAvailability(value.Input)
	value.Output = boundedAvailability(value.Output)
	return value
}

func terminalStatus(file *os.File) string {
	if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return "available"
	}
	return "unavailable"
}

func normalizedAdapter(value ProbeAdapter, environment []string) ProbeAdapter {
	if value.Zsh == "" || value.Nushell == "" {
		joined := "\x00" + strings.Join(environment, "\x00") + "\x00"
		if value.Zsh == "" {
			value.Zsh = availability(strings.Contains(joined, "\x00ZSH_VERSION="))
		}
		if value.Nushell == "" {
			value.Nushell = availability(strings.Contains(joined, "\x00NU_VERSION="))
		}
	}
	value.Zsh, value.Nushell = boundedAvailability(value.Zsh), boundedAvailability(value.Nushell)
	return value
}

func normalizedWindows(value ProbeWindows) ProbeWindows {
	if runtime.GOOS != "windows" {
		return ProbeWindows{Job: "not-applicable", ConPTY: "not-applicable"}
	}
	if value.Job == "" || value.ConPTY == "" {
		detected := detectWindowsCapabilities()
		if value.Job == "" {
			value.Job = detected.Job
		}
		if value.ConPTY == "" {
			value.ConPTY = detected.ConPTY
		}
	}
	return ProbeWindows{Job: boundedCapability(value.Job), ConPTY: boundedCapability(value.ConPTY)}
}

func boundedCapability(value string) string {
	if value == "available" || value == "unavailable" || value == "unknown" || value == "not-applicable" {
		return value
	}
	return "unknown"
}

func boundedAvailability(value string) string {
	if value == "available" || value == "unavailable" {
		return value
	}
	return "unavailable"
}

func availability(ok bool) string {
	if ok {
		return "available"
	}
	return "unavailable"
}

func boundedVersion(value string) string {
	if len(value) > 64 {
		return ""
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && character != '.' {
			return ""
		}
	}
	return value
}

func validToolName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
