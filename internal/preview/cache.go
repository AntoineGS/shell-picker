package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const (
	maxCachedArtifactBytes = int64(64 << 20)
	defaultCacheBytes      = int64(512 << 20)
	cacheTempPrefix        = ".shell-picker-preview-"
)

var ErrUnsafeCache = errors.New("preview: unsafe cache filesystem object")

type Cache struct {
	root     string
	maxBytes int64
}

func NewCache(root string, maximumBytes int64) (*Cache, error) {
	if root == "" {
		root = defaultCacheRoot()
	}
	if maximumBytes <= 0 {
		maximumBytes = defaultCacheBytes
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("preview: resolve cache root: %w", err)
	}
	if err := ensureSafeCacheRoot(absolute); err != nil {
		return nil, err
	}
	return &Cache{root: absolute, maxBytes: maximumBytes}, nil
}

func defaultCacheRoot() string {
	if local := os.Getenv("LOCALAPPDATA"); runtime.GOOS == "windows" && local != "" {
		return filepath.Join(local, "shell-picker", "previews")
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".cache")
		}
	}
	return filepath.Join(base, "shell-picker", "previews")
}

func ensureSafeCacheRoot(root string) error {
	if err := inspectCacheRoot(root, true); err != nil {
		return err
	}
	return inspectCacheRoot(root, false)
}

func inspectCacheRoot(root string, create bool) error {
	volume := filepath.VolumeName(root)
	current := volume + string(os.PathSeparator)
	remainder := strings.TrimPrefix(root, current)
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if create && errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, 0o700); err == nil || errors.Is(err, os.ErrExist) {
				info, err = os.Lstat(current)
			}
		}
		if err != nil || !safeCacheObject(current, info, true) {
			return ErrUnsafeCache
		}
	}
	return nil
}

func (cache *Cache) Key(candidate protocol.ResolvedCandidate, renderer string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte{1})
	writeHashUint64(hash, uint64(len(candidate.Path)))
	_, _ = hash.Write(candidate.Path)
	writeHashUint64(hash, uint64(candidate.Size))
	writeHashUint64(hash, uint64(candidate.ModTimeUnixNano))
	writeHashUint64(hash, uint64(len(renderer)))
	_, _ = hash.Write([]byte(renderer))
	return hex.EncodeToString(hash.Sum(nil))
}

func (cache *Cache) Get(key string) (string, bool) {
	if !validCacheKey(key) || inspectCacheRoot(cache.root, false) != nil {
		return "", false
	}
	path := filepath.Join(cache.root, key)
	if _, err := validateCacheArtifact(path); err != nil {
		return "", false
	}
	if err := refreshCacheTime(path); err != nil {
		return "", false
	}
	if _, err := validateCacheArtifact(path); err != nil {
		return "", false
	}
	return path, true
}

func (cache *Cache) Put(key string, source io.Reader) (path string, resultErr error) {
	if !validCacheKey(key) || inspectCacheRoot(cache.root, false) != nil {
		return "", ErrUnsafeCache
	}
	temp, err := os.CreateTemp(cache.root, cacheTempPrefix)
	if err != nil {
		return "", fmt.Errorf("preview: create cache temporary: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	written, copyErr := io.Copy(temp, io.LimitReader(source, maxCachedArtifactBytes+1))
	if copyErr == nil && written > maxCachedArtifactBytes {
		copyErr = ErrArtifactLimit
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	closeErr := temp.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if _, err := validateCacheArtifact(tempPath); err != nil {
		return "", err
	}
	target := filepath.Join(cache.root, key)
	if _, err := publishNoReplace(tempPath, target); err != nil {
		return "", err
	}
	if _, err := validateCacheArtifact(target); err != nil {
		return "", ErrUnsafeCache
	}
	if err := cache.Prune(); err != nil {
		return "", err
	}
	return target, nil
}

func validateCacheArtifact(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !safeCacheObject(path, info, false) {
		return nil, ErrUnsafeCache
	}
	if info.Size() > maxCachedArtifactBytes {
		return nil, ErrArtifactLimit
	}
	return info, nil
}

func (cache *Cache) Prune() error {
	if err := inspectCacheRoot(cache.root, false); err != nil {
		return err
	}
	entries, err := os.ReadDir(cache.root)
	if err != nil {
		return err
	}
	type artifact struct {
		path  string
		size  int64
		mtime time.Time
	}
	artifacts := make([]artifact, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !validCacheKey(entry.Name()) {
			continue
		}
		path := filepath.Join(cache.root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !safeCacheObject(path, info, false) || info.Size() > maxCachedArtifactBytes {
			continue
		}
		total += info.Size()
		artifacts = append(artifacts, artifact{path: path, size: info.Size(), mtime: info.ModTime()})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].mtime.Before(artifacts[j].mtime) })
	for _, item := range artifacts {
		if total <= cache.maxBytes {
			break
		}
		if err := removeSafeCacheArtifact(item.path); err == nil || errors.Is(err, os.ErrNotExist) {
			total -= item.size
		}
	}
	return nil
}

func validCacheKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, value := range key {
		if value < '0' || value > '9' && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

type richConverter struct{ identity, tool, suffix string }

func richConverterFor(category Category) (richConverter, bool) {
	switch category {
	case CategoryPDF:
		return richConverter{"pdf-pdftoppm-v1", "pdftoppm", ".jpg"}, true
	case CategoryVideo:
		return richConverter{"video-ffmpegthumbnailer-v1", "ffmpegthumbnailer", ".jpg"}, true
	case CategoryAudio:
		return richConverter{"audio-ffmpeg-cover-v1", "ffmpeg", ".jpg"}, true
	default:
		return richConverter{}, false
	}
}

func renderCachedArtifact(ctx context.Context, candidate protocol.ResolvedCandidate, category Category, options Options,
	stdout *budgetWriter, stderr io.Writer, session *renderSession) (bool, bool, error) {
	converter, ok := richConverterFor(category)
	if !ok {
		return false, false, nil
	}
	cache := options.Cache
	if cache == nil {
		var err error
		if cache, err = NewCache("", defaultCacheBytes); err != nil {
			return false, false, nil
		}
	}
	key := cache.Key(candidate, converter.identity)
	if artifact, hit := cache.Get(key); hit {
		staged, cleanup, err := stageCacheArtifact(cache, artifact)
		if err != nil {
			return false, true, nil
		}
		session.cleanup = cleanup
		defer func() { cleanup(); session.cleanup = nil }()
		rendered, err := renderExternal(ctx, staged, CategoryImage, options, stdout, stderr, session)
		return rendered, true, err
	}
	executable := lookupTool(converter.tool, options.Environment)
	if executable == "" {
		return false, false, nil
	}
	directory, err := os.MkdirTemp(cache.root, cacheTempPrefix)
	if err != nil {
		return false, false, nil
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	session.cleanup = cleanup
	defer func() { cleanup(); session.cleanup = nil }()
	artifact := filepath.Join(directory, "artifact"+converter.suffix)
	file, err := os.OpenFile(artifact, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, false, nil
	}
	if err = file.Close(); err != nil {
		return false, false, nil
	}
	arguments := richConverterArguments(category, string(candidate.Path), artifact)
	child, err := options.Runner.Start(ctx, externalProcessSpec(executable, arguments, options.Environment, stdout, stderr))
	if err != nil {
		return false, false, nil
	}
	first, retainErr := session.start(child)
	if first && options.OnDispatch != nil {
		options.OnDispatch(converter.tool, child.PID(), 0)
	}
	wait := make(chan error, 1)
	go func() { wait <- child.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var waitErr error
	for waitErr == nil {
		select {
		case waitErr = <-wait:
			if waitErr == nil {
				waitErr = io.EOF
			}
		case <-ticker.C:
			if info, statErr := os.Lstat(artifact); statErr == nil && info.Size() > maxCachedArtifactBytes {
				_ = os.RemoveAll(directory)
				if session.tree != nil {
					_ = session.tree.KillTree()
				}
				<-wait
				return false, false, session.terminal(ErrArtifactLimit)
			}
		}
	}
	if errors.Is(waitErr, io.EOF) {
		waitErr = nil
	}
	if retainErr != nil {
		return false, false, session.terminal(retainErr)
	}
	if resourceErr := resourceFailure(ctx, stdout.budget, waitErr); resourceErr != nil {
		return false, false, session.terminal(resourceErr)
	}
	info, validationErr := validateCacheArtifact(artifact)
	if errors.Is(validationErr, os.ErrNotExist) {
		return false, false, nil
	}
	if validationErr != nil {
		return false, false, session.terminal(validationErr)
	}
	if info.Size() == 0 {
		return false, false, nil
	}
	source, err := openSafeCacheArtifact(artifact)
	if err != nil {
		return false, false, session.terminal(err)
	}
	_, putErr := cache.Put(key, source)
	closeErr := source.Close()
	if putErr != nil || closeErr != nil {
		return false, false, session.terminal(errors.Join(putErr, closeErr))
	}
	rendered, err := renderExternal(ctx, artifact, CategoryImage, options, stdout, stderr, session)
	return rendered, true, err
}
