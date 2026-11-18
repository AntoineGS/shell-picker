package integration

import "runtime"

func expectedIndexedSourceLabel() string {
	if runtime.GOOS == "linux" {
		return "Unearth"
	}
	return "FSearch"
}
