package preview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
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
var cacheGetenv, cacheUserHome = os.Getenv, os.UserHomeDir

type Cache struct {
	root         string
	maxBytes     int64
	rootIdentity fileIdentity
}

type fileIdentity struct{ first, second uint64 }

type cacheSource struct {
	file     *os.File
	identity fileIdentity
}

func (source *cacheSource) Read(data []byte) (int, error) { return source.file.Read(data) }
func (source *cacheSource) Close() error                  { return source.file.Close() }
func (artifact *converterArtifact) Path() string          { return artifact.path }

func NewCache(root string, maximumBytes int64) (*Cache, error) {
	var err error
	if root == "" {
		root, err = defaultCacheRoot()
		if err != nil {
			return nil, err
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootIdentity, err := ensureCacheRoot(absolute)
	if err != nil {
		return nil, err
	}
	if maximumBytes <= 0 {
		maximumBytes = defaultCacheBytes
	}
	return &Cache{root: absolute, maxBytes: maximumBytes, rootIdentity: rootIdentity}, nil
}

func defaultCacheRoot() (string, error) {
	base := cacheGetenv(cacheEnvironmentVariable)
	if base == "" {
		home, err := cacheUserHome()
		if err != nil || home == "" {
			return "", errors.New("preview: cache home unavailable")
		}
		base = filepath.Join(home, cacheHomeSuffix)
	}
	return filepath.Join(base, "shell-picker", "previews"), nil
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
	if !validCacheKey(key) {
		return "", false
	}
	source, err := openCacheSource(cache, key)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		return "", false
	}
	defer source.Close()
	err = source.Validate()
	return filepath.Join(cache.root, key), err == nil
}

func (cache *Cache) Put(key string, source io.Reader) (string, error) {
	if !validCacheKey(key) {
		return "", ErrUnsafeCache
	}
	if err := cachePut(cache, key, source); err != nil {
		return "", err
	}
	_ = cachePrune(cache)
	return filepath.Join(cache.root, key), nil
}

func (cache *Cache) Prune() error { return cachePrune(cache) }

func validCacheKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil && key == strings.ToLower(key)
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
	if _, hit := cache.Get(key); hit {
		return renderStagedCache(ctx, cache, key, options, stdout, stderr, session)
	}
	executable := lookupTool(converter.tool, options.Environment)
	if executable == "" {
		return false, false, nil
	}
	temp, err := newConverterArtifact(cache, converter.suffix)
	if err != nil {
		return false, false, nil
	}
	session.cleanup = temp.Cleanup
	defer func() {
		if temp.Cleanup() {
			session.cleanup = nil
		}
	}()
	child, err := options.Runner.Start(ctx, externalProcessSpec(executable,
		richConverterArguments(category, string(candidate.Path), temp.Path()), options.Environment, stdout, stderr))
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
	waitErr, terminalErr := waitConverter(wait, ticker.C, temp, session)
	if terminalErr != nil {
		return false, false, terminalErr
	}
	if retainErr != nil {
		return false, false, session.terminal(retainErr)
	}
	if resourceErr := resourceFailure(ctx, stdout.budget, waitErr); resourceErr != nil {
		return false, false, session.terminal(resourceErr)
	}
	source, size, err := temp.OpenAccepted()
	if errors.Is(err, os.ErrNotExist) || err == nil && size == 0 {
		if source != nil {
			_ = source.Close()
		}
		return false, false, nil
	}
	if err != nil {
		return false, false, session.terminal(err)
	}
	_, putErr := cache.Put(key, source)
	closeErr := source.Close()
	if putErr != nil || closeErr != nil {
		return false, false, session.terminal(errors.Join(putErr, closeErr))
	}
	return renderStagedCache(ctx, cache, key, options, stdout, stderr, session)
}

func waitConverter(wait <-chan error, ticks <-chan time.Time, temp *converterArtifact, session *renderSession) (error, error) {
	for {
		select {
		case err := <-wait:
			return err, nil
		case <-ticks:
			if size, err := temp.Size(); err == nil && size > maxCachedArtifactBytes {
				_ = temp.Cleanup()
				if session.tree != nil {
					_ = session.tree.KillTree()
				}
				<-wait
				_ = temp.Cleanup()
				return nil, session.terminal(ErrArtifactLimit)
			}
		}
	}
}

func renderStagedCache(ctx context.Context, cache *Cache, key string, options Options, stdout *budgetWriter,
	stderr io.Writer, session *renderSession) (bool, bool, error) {
	staged, err := stageCacheArtifact(cache, key)
	if err != nil {
		return false, true, nil
	}
	session.cleanup = staged.Cleanup
	defer func() {
		if staged.Cleanup() {
			session.cleanup = nil
		}
	}()
	rendered, err := renderExternal(ctx, staged.Path(), CategoryImage, options, stdout, stderr, session, staged.RenderFiles()...)
	return rendered, true, err
}

type syncWriteCloser interface {
	io.Writer
	Sync() error
	Close() error
}

func stageCacheArtifact(cache *Cache, key string) (*converterArtifact, error) {
	source, err := openCacheSource(cache, key)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	stage, err := newConverterArtifact(cache, ".jpg")
	if err != nil {
		return nil, err
	}
	destination, err := stage.OpenWritable()
	if err == nil {
		var written int64
		written, err = io.Copy(destination, io.LimitReader(source, maxCachedArtifactBytes+1))
		if err == nil && written > maxCachedArtifactBytes {
			err = ErrArtifactLimit
		}
	}
	if err == nil {
		err = destination.Sync()
	}
	if destination != nil {
		err = errors.Join(err, destination.Close())
	}
	if err == nil {
		err = source.Validate()
	}
	if err == nil {
		err = stage.Validate()
	}
	if err != nil {
		_ = stage.Cleanup()
		return nil, err
	}
	return stage, nil
}

func randomCacheName(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func mapIdentity(published bool, identity fileIdentity) fileIdentity {
	if published {
		return identity
	}
	return fileIdentity{}
}

type cacheSyncWriter interface {
	io.Writer
	Sync() error
}

func copyCacheData(destination cacheSyncWriter, source io.Reader) error {
	written, err := io.Copy(destination, io.LimitReader(source, maxCachedArtifactBytes+1))
	if err == nil && written > maxCachedArtifactBytes {
		err = ErrArtifactLimit
	}
	if err == nil {
		err = destination.Sync()
	}
	return err
}

type pruneItem struct {
	name     string
	size     int64
	modified time.Time
	identity fileIdentity
}

func pruneOldest(maximum, total int64, items []pruneItem, remove func(pruneItem) bool) {
	sort.Slice(items, func(i, j int) bool { return items[i].modified.Before(items[j].modified) })
	for _, item := range items {
		if total <= maximum {
			break
		}
		if remove(item) {
			total -= item.size
		}
	}
}

func saturatedAdd(total, size int64) int64 {
	if size > int64(^uint64(0)>>1)-total {
		return int64(^uint64(0) >> 1)
	}
	return total + size
}
