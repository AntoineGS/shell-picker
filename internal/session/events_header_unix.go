//go:build !windows

package session

func addErrorHeader(State) string { return "" }
