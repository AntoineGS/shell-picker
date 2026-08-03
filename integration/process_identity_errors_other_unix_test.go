//go:build !linux && !windows

package integration

func isTransientProcessIdentityError(error) bool { return false }
