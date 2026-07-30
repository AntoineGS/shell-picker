package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/process"
)

func measureBaseline(t *testing.T, name string, samples int, operation func() error) integrationpkg.BaselineMetric {
	t.Helper()
	values := make([]float64, samples)
	for index := range samples {
		started := time.Now()
		if err := operation(); err != nil {
			t.Fatalf("baseline %s sample %d: %v", name, index+1, err)
		}
		values[index] = float64(time.Since(started).Microseconds())
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		variance += (value - mean) * (value - mean)
	}
	variance /= float64(len(values))
	return integrationpkg.BaselineMetric{Name: name, MeanUS: mean, StdDevUS: math.Sqrt(variance)}
}

func measureLoopbackBaseline(t *testing.T, samples int) integrationpkg.BaselineMetric {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	return measureBaseline(t, "loopback-http", samples, func() error {
		response, err := server.Client().Get(server.URL)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, response.Body)
		return response.Body.Close()
	})
}

func measureReadDirBaseline(t *testing.T, samples int) integrationpkg.BaselineMetric {
	t.Helper()
	root := t.TempDir()
	for index := range 1000 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%04d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.ReadDir(root); err != nil {
		t.Fatal(err)
	}
	return measureBaseline(t, "warm-readdir-1000", samples, func() error {
		entries, err := os.ReadDir(root)
		if err == nil && len(entries) != 1000 {
			return fmt.Errorf("readdir entries=%d", len(entries))
		}
		return err
	})
}

func collectBenchmarkMetadata(t *testing.T, binary string) integrationpkg.BenchmarkMetadata {
	t.Helper()
	hostname, _ := os.Hostname()
	metadata := integrationpkg.BenchmarkMetadata{Hostname: boundedMetadata(hostname), OS: runtime.GOOS, Arch: runtime.GOARCH,
		CPUModel: "unavailable", CPUCount: runtime.NumCPU(), CPUGovernor: "unavailable", PowerPlan: "not-applicable",
		Memory: "unavailable", Filesystem: "unavailable", Terminal: boundedMetadata(os.Getenv("TERM")),
		FZFVersion: commandVersion("fzf", "--version"), GoVersion: runtime.Version(), Antivirus: "unavailable", Power: "unavailable"}
	if runtime.GOOS == "linux" {
		metadata.CPUModel = linuxMetadataValue("/proc/cpuinfo", "model name")
		metadata.Memory = linuxMetadataValue("/proc/meminfo", "MemTotal")
		metadata.CPUGovernor = firstFileValue("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor")
		metadata.Power = linuxPowerState()
	}
	filesystem, powerPlan, antivirus := platformBenchmarkMetadata(binary)
	metadata.Filesystem = filesystem
	metadata.PowerPlan = powerPlan
	metadata.Antivirus = antivirus
	if version := commandVersion(binary, "version"); version == "unavailable" {
		t.Fatal("prebuilt picker version command failed")
	}
	return metadata
}

func commandVersion(path string, argument string) string {
	command := exec.Command(path, argument)
	command.Env = process.SanitizeEnv(os.Environ(), nil)
	output, err := command.Output()
	if err != nil {
		return "unavailable"
	}
	return boundedMetadata(strings.TrimSpace(string(output)))
}

func linuxMetadataValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) == key {
			return boundedMetadata(strings.TrimSpace(value))
		}
	}
	return "unavailable"
}

func firstFileValue(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	return boundedMetadata(strings.TrimSpace(string(data)))
}

func linuxPowerState() string {
	entries, err := filepath.Glob("/sys/class/power_supply/*/online")
	if err != nil || len(entries) == 0 {
		return "unavailable"
	}
	for _, entry := range entries {
		if firstFileValue(entry) == "1" {
			return "ac"
		}
	}
	return "battery"
}

func boundedMetadata(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	if value == "" {
		return "unavailable"
	}
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}

func readHostBaseline(t *testing.T, path string) integrationpkg.HostBaseline {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return integrationpkg.HostBaseline{}
	}
	var baseline integrationpkg.HostBaseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	return baseline
}

func writePerformanceJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
