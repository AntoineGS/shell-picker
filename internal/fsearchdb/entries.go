package fsearchdb

import (
	"bytes"
	"context"
	"fmt"
)

const (
	maxEntryName   = 1 << 20
	maxPathBytes   = 1 << 30
	maxEntries     = 20_000_000
	maxConfigItems = 1_000_000
)

type rawEntry struct {
	name     []byte
	parent   uint32
	size     int64
	mtime    int64
	hasMTime bool
}

func makePaths(ctx context.Context, folders, files []rawEntry) ([]Entry, error) {
	paths := make([][]byte, len(folders))
	state := make([]uint8, len(folders)) // 0 unseen, 1 resolving, 2 complete
	var total uint64

	for start := range folders {
		if start&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if state[start] == 2 {
			continue
		}
		chain := make([]int, 0, 16)
		cur := start
		var base []byte
		depth := 0
		for {
			if depth&0x3ff == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			depth++
			switch state[cur] {
			case 2:
				base = paths[cur]
				goto unwind
			case 1:
				return nil, fmt.Errorf("%w: folder parent cycle at index %d", ErrMalformed, cur)
			}
			state[cur] = 1
			chain = append(chain, cur)
			p := folders[cur].parent
			if p == uint32(cur) || p == ^uint32(0) {
				if len(folders[cur].name) == 0 || folders[cur].name[0] != '/' || bytes.IndexByte(folders[cur].name, 0) >= 0 {
					return nil, fmt.Errorf("%w: root folder path is not absolute", ErrMalformed)
				}
				base = nil
				break
			}
			if len(folders[cur].name) == 0 || bytes.IndexByte(folders[cur].name, '/') >= 0 {
				return nil, fmt.Errorf("%w: invalid folder name component", ErrMalformed)
			}
			cur = int(p)
		}
	unwind:
		for i := len(chain) - 1; i >= 0; i-- {
			if (len(chain)-1-i)&0x3ff == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			idx := chain[i]
			name := folders[idx].name
			var path []byte
			if base == nil {
				path = append([]byte(nil), name...)
			} else if len(base) == 1 && base[0] == '/' {
				path = make([]byte, 0, len(base)+len(name))
				path = append(path, '/')
				path = append(path, name...)
			} else {
				path = make([]byte, 0, len(base)+1+len(name))
				path = append(path, base...)
				path = append(path, '/')
				path = append(path, name...)
			}
			if len(path) > maxEntryName*4 {
				return nil, fmt.Errorf("%w: reconstructed path exceeds limit", ErrMalformed)
			}
			paths[idx] = path
			state[idx] = 2
			base = path
			total += uint64(len(path))
			if total > maxPathBytes {
				return nil, fmt.Errorf("%w: aggregate path data exceeds limit", ErrMalformed)
			}
		}
	}

	out := make([]Entry, 0, len(folders)+len(files))
	for i, f := range folders {
		if i&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		out = append(out, Entry{Path: append([]byte(nil), paths[i]...), IsDir: true, Size: f.size, ModTime: unixTime(f.mtime, f.hasMTime)})
	}
	for i, f := range files {
		if i&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if f.parent >= uint32(len(folders)) {
			return nil, fmt.Errorf("%w: file parent index out of range", ErrMalformed)
		}
		if bytes.IndexByte(f.name, 0) >= 0 || len(f.name) == 0 || bytes.IndexByte(f.name, '/') >= 0 {
			return nil, fmt.Errorf("%w: invalid filename component", ErrMalformed)
		}
		parent := paths[f.parent]
		n := len(parent) + 1 + len(f.name)
		total += uint64(n)
		if total > maxPathBytes {
			return nil, fmt.Errorf("%w: aggregate path data exceeds limit", ErrMalformed)
		}
		path := make([]byte, 0, n)
		path = append(path, parent...)
		if len(parent) != 1 || parent[0] != '/' {
			path = append(path, '/')
		}
		path = append(path, f.name...)
		out = append(out, Entry{Path: path, Size: f.size, ModTime: unixTime(f.mtime, f.hasMTime)})
	}
	return out, nil
}
