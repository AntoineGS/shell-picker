//go:build !windows

package integration

import "testing"

type task20HandleScope struct{}

func beginTask20HandleScope(*testing.T, string) task20HandleScope { return task20HandleScope{} }
func (task20HandleScope) Capture(*testing.T)                      {}
func (task20HandleScope) RequireClosed(*testing.T)                {}
