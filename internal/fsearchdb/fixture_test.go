package fsearchdb

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
)

func fixture(order binary.ByteOrder, little bool, folderNames [][]byte, folderParents []uint32, files []testFile, flags uint64) []byte {
	return fixtureWithConfig(order, little, folderNames, folderParents, files, flags, nil, nil)
}

func fixtureWithConfig(order binary.ByteOrder, little bool, folderNames [][]byte, folderParents []uint32, files []testFile, flags uint64, includes, excludes []string) []byte {
	var b bytes.Buffer
	b.WriteString("FSDB")
	b.Write([]byte{6, 0, 0})
	if little {
		b.Bytes()[6] = 1
	}
	put := func(v any) { _ = binary.Write(&b, order, v) }
	put(flags)
	put(uint64(0))
	put(uint32(len(includes)))
	for _, path := range includes {
		put(uint32(0)) // include type
		put(uint32(len(path)))
		b.WriteString(path)
		b.Write([]byte{1, 1, 0, 1}) // one filesystem, active, monitored, scan after launch
		put(int64(10))
		put(uint32(11))
		put(int64(12))
		put(uint32(13))
		put(uint32(14))
		put(uint32(15))
		b.WriteByte(1) // scan reason
	}
	put(uint32(len(excludes)))
	for _, pattern := range excludes {
		put(uint32(0)) // record type
		b.Write([]byte{1, 2, 3})
		put(uint32(len(pattern)))
		b.WriteString(pattern)
		b.WriteByte(1) // active
	}
	b.WriteByte(1) // exclude hidden
	var fb, xb bytes.Buffer
	writeEntries := func(dst *bytes.Buffer, names [][]byte, parents []uint32) {
		var prev []byte
		for i, name := range names {
			prefix := 0
			for prefix < len(prev) && prefix < len(name) && prev[prefix] == name[prefix] {
				prefix++
			}
			binary.Write(dst, order, uint16(prefix))
			binary.Write(dst, order, uint16(len(name)-prefix))
			dst.Write(name[prefix:])
			if flags&(1<<2) != 0 {
				binary.Write(dst, order, uint64(i*10+1))
			}
			if flags&(1<<3) != 0 {
				binary.Write(dst, order, int64(1700000000+i))
			}
			binary.Write(dst, order, parents[i])
			prev = name
		}
	}
	writeEntries(&fb, folderNames, folderParents)
	fileNames := make([][]byte, len(files))
	fileParents := make([]uint32, len(files))
	for i, f := range files {
		fileNames[i], fileParents[i] = f.name, f.parent
	}
	writeEntries(&xb, fileNames, fileParents)
	put(uint32(len(folderNames)))
	put(uint32(len(files)))
	put(uint64(fb.Len()))
	put(uint64(xb.Len()))
	sum := md5.Sum(b.Bytes())
	b.Write(sum[:])
	b.Write(fb.Bytes())
	b.Write(xb.Bytes())
	put(uint32(0)) // no sorted arrays
	return b.Bytes()
}

type testFile struct {
	name   []byte
	parent uint32
}
