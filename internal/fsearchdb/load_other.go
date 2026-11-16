//go:build !linux

package fsearchdb

import (
	"context"
	"fmt"
)

// Load is only supported on Linux, where FSearch's lock can be honored.
func Load(context.Context, string) (*Database, error) {
	return nil, fmt.Errorf("%w: loading FSearch databases requires Linux", ErrUnsupported)
}
