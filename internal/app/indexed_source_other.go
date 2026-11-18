//go:build !linux

package app

import (
	"context"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
)

func loadDefaultIndex(ctx context.Context, home string, environment []string) (*fsearchdb.Database, error) {
	values := make(map[string]string)
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	path := fsearchdb.DefaultPath(home, func(key string) string { return values[key] })
	return fsearchdb.Load(ctx, path)
}
