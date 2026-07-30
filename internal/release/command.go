package release

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const maxEntrySize = 32 << 20

// maxArchiveSize bounds checksum and rebuild I/O while allowing four release
// binaries, documentation, and archive metadata with substantial headroom.
const maxArchiveSize int64 = 128 << 20

type target struct{ goos, goarch, suffix string }

var targets = []target{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "windows", goarch: "amd64", suffix: ".exe"},
	{goos: "windows", goarch: "arm64", suffix: ".exe"},
}

func Main() {
	if len(os.Args) < 2 {
		fatal("usage: release.go snapshot VERSION [GOOS GOARCH] | check [VERSION]")
	}
	version := ""
	switch os.Args[1] {
	case "snapshot":
		if len(os.Args) < 3 {
			fatal("snapshot requires VERSION")
		}
		version = os.Args[2]
		validateVersion(version)
		when := releaseTime()
		if len(os.Args) == 5 {
			one(version, os.Args[3], os.Args[4], when)
			return
		}
		if len(os.Args) != 3 {
			fatal("snapshot accepts either no target or GOOS GOARCH")
		}
		resetDist()
		for _, item := range targets {
			one(version, item.goos, item.goarch, when)
		}
		writeChecksumsTo("dist")
	case "check":
		if len(os.Args) > 3 {
			fatal("check accepts at most VERSION")
		}
		if len(os.Args) == 3 {
			version = os.Args[2]
			validateVersion(version)
		}
		if version == "" {
			version = inferVersion()
		}
		check(version)
	case "checksums":
		if len(os.Args) != 3 {
			fatal("checksums requires DIRECTORY")
		}
		checksumArtifacts(os.Args[2])
	default:
		fatal("unknown operation")
	}
}

func validateVersion(version string) {
	if !strings.HasPrefix(version, "v") || strings.ContainsAny(version, " /\\") {
		fatal("version must be a v-prefixed tag")
	}
}

func releaseTime() time.Time {
	if value := os.Getenv("SOURCE_DATE_EPOCH"); value != "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			fatal(fmt.Sprintf("invalid SOURCE_DATE_EPOCH: %v", err))
		}
		return time.Unix(seconds, 0).UTC()
	}
	command := exec.Command("git", "show", "-s", "--format=%ct", "HEAD")
	output, err := command.Output()
	if err != nil {
		fatal(err.Error())
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		fatal(err.Error())
	}
	return time.Unix(seconds, 0).UTC()
}

func resetDist() {
	if err := os.RemoveAll("dist"); err != nil {
		fatal(err.Error())
	}
	if err := os.MkdirAll("dist", 0o755); err != nil {
		fatal(err.Error())
	}
}

func findTarget(goos, goarch string) target {
	for _, item := range targets {
		if item.goos == goos && item.goarch == goarch {
			return item
		}
	}
	fatal("unsupported target")
	return target{}
}

func archiveName(version string, item target) string {
	extension := map[bool]string{true: "zip", false: "tar.gz"}[item.goos == "windows"]
	return fmt.Sprintf("shell-picker_%s_%s_%s.%s", strings.TrimPrefix(version, "v"), item.goos, item.goarch, extension)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
