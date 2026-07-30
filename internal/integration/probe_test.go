package integration

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestProbeReportsHardAndSoftDependenciesWithoutLeakingPaths(t *testing.T) {
	lookups := map[string]string{
		"fzf": "/private/tools/fzf",
		"bat": "/private/tools/bat",
	}
	report := Probe(context.Background(), ProbeOptions{
		Version:      "v1.2.3",
		FZFPath:      "fzf",
		ZoxidePath:   "zoxide",
		PreviewTools: []string{"missing-tool", "bat"},
		LookupPath: func(name string) (string, error) {
			path, ok := lookups[name]
			if !ok {
				return "", errors.New("not found")
			}
			return path, nil
		},
		Environment: []string{"PATH=/tools", "FZF_DEFAULT_OPTS=--bind=start:abort", "SHELL_PICKER_TOKEN=stale"},
		CheckFZF: func(_ context.Context, _ string, environment []string) (string, error) {
			for _, entry := range environment {
				if entry == "FZF_DEFAULT_OPTS=--bind=start:abort" || entry == "SHELL_PICKER_TOKEN=stale" {
					t.Fatalf("probe child inherited unsafe environment: %q", entry)
				}
			}
			return "0.74.1", nil
		},
		Cache:                ProbeCache{Root: "/private/cache", Status: "writable"},
		Terminal:             ProbeTerminal{Input: "available", Output: "available"},
		Adapter:              ProbeAdapter{Zsh: "available", Nushell: "unavailable"},
		DefaultZoxideTimeout: 75 * time.Millisecond,
	})

	if !report.Ready || report.Schema != 1 || report.ApplicationVersion != "v1.2.3" {
		t.Fatalf("report=%+v", report)
	}
	if report.FZF.Version != "0.74.1" || report.FZF.Status != "ready" || report.FZF.Path != "sha256:c765a20997211dbe" {
		t.Fatalf("fzf=%+v", report.FZF)
	}
	if report.Zoxide.Status != "optional-missing" || report.Zoxide.DefaultPolicy != "cached" ||
		report.Zoxide.ExactParity != "--zoxide-policy fresh --zoxide-timeout 0" || report.Zoxide.DefaultTimeout != "75ms" {
		t.Fatalf("zoxide=%+v", report.Zoxide)
	}
	wantTools := map[string]ProbeDependency{
		"bat":          {Status: "available", Path: "sha256:cb9bbba892478039"},
		"missing-tool": {Status: "optional-missing"},
	}
	if !reflect.DeepEqual(report.Tools, wantTools) {
		t.Fatalf("tools=%+v want %+v", report.Tools, wantTools)
	}
	if report.Cache.Root != "sha256:ad47e4d05532b1ea" || report.Cache.Status != "writable" {
		t.Fatalf("cache=%+v", report.Cache)
	}
}

func TestProbeMakesFZFReadinessHardAndOptionalAbsenceSoft(t *testing.T) {
	report := Probe(context.Background(), ProbeOptions{
		FZFPath: "fzf", ZoxidePath: "zoxide",
		LookupPath: func(name string) (string, error) {
			if name == "fzf" {
				return "/tools/fzf", nil
			}
			return "", errors.New("not found")
		},
		CheckFZF: func(context.Context, string, []string) (string, error) {
			return "0.73.0", errors.New("below minimum")
		},
	})
	if report.Ready || report.FZF.Status != "version-unsupported" || report.Zoxide.Status != "optional-missing" {
		t.Fatalf("report=%+v", report)
	}
}

func TestProbeRejectsUnboundedInjectedStatuses(t *testing.T) {
	report := Probe(context.Background(), ProbeOptions{
		FZFPath:    "fzf",
		LookupPath: func(string) (string, error) { return "/tools/fzf", nil },
		CheckFZF:   func(context.Context, string, []string) (string, error) { return "0.74.1", nil },
		Cache:      ProbeCache{Status: "attacker-controlled"},
	})
	if report.Cache.Status != "unknown" {
		t.Fatalf("cache status=%q", report.Cache.Status)
	}
}

func TestProbeCacheRootMatchesPlatformPreviewDefaults(t *testing.T) {
	tests := []struct {
		goos   string
		home   string
		values map[string]string
		want   string
	}{
		{goos: "linux", home: "/home/user", values: map[string]string{}, want: "/home/user/.cache/shell-picker/previews"},
		{goos: "linux", home: "/home/user", values: map[string]string{"XDG_CACHE_HOME": "/cache"}, want: "/cache/shell-picker/previews"},
		{goos: "windows", home: `C:\Users\user`, values: map[string]string{}, want: `C:\Users\user/AppData/Local/shell-picker/previews`},
		{goos: "windows", home: `C:\Users\user`, values: map[string]string{"LOCALAPPDATA": `D:\cache`}, want: `D:\cache/shell-picker/previews`},
	}
	for _, test := range tests {
		if got := probeCacheRoot(test.goos, test.home, test.values); got != test.want {
			t.Errorf("probeCacheRoot(%s)=%q want %q", test.goos, got, test.want)
		}
	}
}
