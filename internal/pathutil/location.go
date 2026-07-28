package pathutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

type Kind uint8

const (
	KindFilesystem Kind = iota + 1
	KindDrives
)

var (
	ErrInvalidAdd      = errors.New("invalid add query")
	ErrUnsafeTraversal = errors.New("unsafe filesystem traversal")
)

type Location struct {
	Kind Kind
	Path []byte
}

type CreatedTree struct {
	Target  Location
	Created [][]byte
}

func Filesystem(path []byte) Location {
	return Location{Kind: KindFilesystem, Path: bytes.Clone(path)}
}

func Drives() Location {
	return Location{Kind: KindDrives}
}

func ValidateAddQuery(base Location, query []byte) error {
	if base.Kind != KindFilesystem || len(query) == 0 || isAbsolute(query) {
		return ErrInvalidAdd
	}
	for _, part := range splitSeparators(query) {
		if bytes.Equal(part, []byte("..")) {
			return ErrInvalidAdd
		}
	}
	return nil
}

// Rollback removes only directories recorded as created by this operation.
// Nonempty and already-removed directories are deliberately left alone.
func (tree *CreatedTree) Rollback() error {
	if tree == nil {
		return nil
	}
	var rollbackErr error
	for i := len(tree.Created) - 1; i >= 0; i-- {
		if err := os.Remove(string(tree.Created[i])); err != nil && !errors.Is(err, os.ErrNotExist) && !isDirectoryNotEmpty(err) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove %q: %w", tree.Created[i], err))
		}
	}
	tree.Created = nil
	return rollbackErr
}

func rollbackAfterError(tree *CreatedTree, err error) (CreatedTree, error) {
	if rollbackErr := tree.Rollback(); rollbackErr != nil {
		return CreatedTree{}, errors.Join(err, rollbackErr)
	}
	return CreatedTree{}, err
}
