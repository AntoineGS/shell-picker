//go:build darwin

package preview

import (
	"os"

	"golang.org/x/sys/unix"
)

func cachePrune(cache *Cache) error {
	root, err := openCache(cache)
	if err != nil {
		return err
	}
	defer root.Close()
	duplicate, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(duplicate), cache.root)
	entries, err := directory.Readdir(-1)
	_ = directory.Close()
	if err != nil {
		return err
	}
	items := make([]pruneItem, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !validCacheKey(entry.Name()) {
			continue
		}
		file, identity, size, openErr := openAcceptedAt(root, entry.Name(), fileIdentity{})
		if openErr != nil {
			continue
		}
		info, statErr := file.Stat()
		_ = file.Close()
		if statErr != nil {
			continue
		}
		total = saturatedAdd(total, size)
		items = append(items, pruneItem{entry.Name(), size, info.ModTime(), identity})
	}
	pruneOldest(cache.maxBytes, total, items, func(item pruneItem) bool { return quarantinePrune(cache, item) })
	return nil
}

func renameNoReplaceAt(directory int, oldName, newName string) error {
	return unix.RenameatxNp(directory, oldName, directory, newName, unix.RENAME_EXCL)
}

func quarantinePrune(cache *Cache, item pruneItem) bool {
	root, err := openCache(cache)
	if err != nil {
		return false
	}
	defer root.Close()
	quarantine, err := randomCacheName(cacheTempPrefix + "prune-")
	if err != nil || renameNoReplaceAt(int(root.Fd()), item.name, quarantine) != nil {
		return false
	}
	file, _, _, openErr := openAcceptedAt(root, quarantine, item.identity)
	if openErr != nil {
		_ = renameNoReplaceAt(int(root.Fd()), quarantine, item.name)
		return false
	}
	unlinkErr := unix.Unlinkat(int(root.Fd()), quarantine, 0)
	_, links, statErr := validateOpenFile(file, 0, item.identity)
	_ = file.Close()
	return unlinkErr == nil && statErr == nil && links == 0
}
