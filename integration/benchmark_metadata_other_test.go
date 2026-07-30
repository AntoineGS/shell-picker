//go:build !linux && !windows

package integration

func platformBenchmarkMetadata(string) (string, string, string) {
	return "unavailable", "unavailable", "unavailable"
}
