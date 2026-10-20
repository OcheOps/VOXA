package mediastore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxChunkIndex bounds chunk indices (and the number of chunks per upload).
const MaxChunkIndex = 99_999

// Filesystem stores media under a root directory:
//
//	<root>/staging/<upload-key>/000000.part   staged chunks
//	<root>/objects/<object-key>               assembled objects
//
// Writes go to a temporary file, are fsynced and renamed into place, so a
// crash never leaves a partially written chunk or object under its final
// name. Locking is per upload within this process: one server instance owns
// a media directory.
type Filesystem struct {
	root  string
	locks sync.Map // upload key -> *sync.Mutex
}

func NewFilesystem(root string) (*Filesystem, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("media directory: %w", err)
	}
	for _, dir := range []string{"staging", "objects", "tmp"} {
		if err := os.MkdirAll(filepath.Join(absolute, dir), 0o700); err != nil {
			return nil, fmt.Errorf("create media directory: %w", err)
		}
	}
	return &Filesystem{root: absolute}, nil
}

func (f *Filesystem) lock(key string) func() {
	value, _ := f.locks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (f *Filesystem) stagingDir(key string) string { return filepath.Join(f.root, "staging", key) }
func (f *Filesystem) objectPath(key string) string { return filepath.Join(f.root, "objects", key) }
func chunkName(index int) string                   { return fmt.Sprintf("%06d.part", index) }

func checkKey(key string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	return nil
}

func (f *Filesystem) PutChunk(ctx context.Context, uploadKey string, index int, data io.Reader, maxBytes int64) (ChunkInfo, error) {
	if err := checkKey(uploadKey); err != nil {
		return ChunkInfo{}, err
	}
	if index < 0 || index > MaxChunkIndex {
		return ChunkInfo{}, fmt.Errorf("chunk index out of range")
	}
	unlock := f.lock(uploadKey)
	defer unlock()

	dir := f.stagingDir(uploadKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ChunkInfo{}, err
	}
	temp, err := os.CreateTemp(filepath.Join(f.root, "tmp"), "chunk-*")
	if err != nil {
		return ChunkInfo{}, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(contextReader{ctx, data}, maxBytes+1))
	if err == nil && written > maxBytes {
		err = ErrTooLarge
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return ChunkInfo{}, err
	}
	info := ChunkInfo{Index: index, Size: written}
	copy(info.SHA256[:], hash.Sum(nil))

	final := filepath.Join(dir, chunkName(index))
	if existing, err := hashFile(final); err == nil {
		if existing.SHA256 != info.SHA256 {
			return ChunkInfo{}, ErrChunkConflict
		}
		existing.Index = index
		return existing, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ChunkInfo{}, err
	}
	if err := os.Rename(temp.Name(), final); err != nil {
		return ChunkInfo{}, err
	}
	return info, syncDir(dir)
}

func (f *Filesystem) ListChunks(_ context.Context, uploadKey string) ([]ChunkInfo, error) {
	if err := checkKey(uploadKey); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(f.stagingDir(uploadKey))
	if errors.Is(err, fs.ErrNotExist) {
		return []ChunkInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	chunks := make([]ChunkInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".part") {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSuffix(name, ".part"))
		if err != nil || chunkName(index) != name {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, ChunkInfo{Index: index, Size: info.Size()})
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	return chunks, nil
}

func (f *Filesystem) Assemble(ctx context.Context, uploadKey string, count int, objectKey string) (ObjectInfo, error) {
	if err := checkKey(uploadKey); err != nil {
		return ObjectInfo{}, err
	}
	if err := checkKey(objectKey); err != nil {
		return ObjectInfo{}, err
	}
	unlock := f.lock(uploadKey)
	defer unlock()

	temp, err := os.CreateTemp(filepath.Join(f.root, "tmp"), "object-*")
	if err != nil {
		return ObjectInfo{}, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	var size int64
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			temp.Close()
			return ObjectInfo{}, err
		}
		chunk, err := os.Open(filepath.Join(f.stagingDir(uploadKey), chunkName(index)))
		if errors.Is(err, fs.ErrNotExist) {
			temp.Close()
			return ObjectInfo{}, ErrMissingChunk
		}
		if err != nil {
			temp.Close()
			return ObjectInfo{}, err
		}
		n, err := io.Copy(temp, chunk)
		chunk.Close()
		if err != nil {
			temp.Close()
			return ObjectInfo{}, err
		}
		size += n
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return ObjectInfo{}, err
	}
	if err := temp.Close(); err != nil {
		return ObjectInfo{}, err
	}
	final := f.objectPath(objectKey)
	if err := os.Rename(temp.Name(), final); err != nil {
		return ObjectInfo{}, err
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return ObjectInfo{}, err
	}
	stat, err := os.Stat(final)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: size, ModTime: stat.ModTime()}, nil
}

type fileObject struct {
	*os.File
	info ObjectInfo
}

func (o fileObject) Info() ObjectInfo { return o.info }

func (f *Filesystem) Open(_ context.Context, objectKey string) (Object, error) {
	if err := checkKey(objectKey); err != nil {
		return nil, err
	}
	file, err := os.Open(f.objectPath(objectKey))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return fileObject{File: file, info: ObjectInfo{Size: stat.Size(), ModTime: stat.ModTime()}}, nil
}

func (f *Filesystem) DeleteObject(_ context.Context, objectKey string) error {
	if err := checkKey(objectKey); err != nil {
		return err
	}
	if err := os.Remove(f.objectPath(objectKey)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (f *Filesystem) DeleteUpload(_ context.Context, uploadKey string) error {
	if err := checkKey(uploadKey); err != nil {
		return err
	}
	unlock := f.lock(uploadKey)
	defer unlock()
	return os.RemoveAll(f.stagingDir(uploadKey))
}

func (f *Filesystem) list(dir string) ([]UploadInfo, error) {
	entries, err := os.ReadDir(filepath.Join(f.root, dir))
	if err != nil {
		return nil, err
	}
	var items []UploadInfo
	for _, entry := range entries {
		if !ValidKey(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, UploadInfo{Key: entry.Name(), ModTime: info.ModTime()})
	}
	return items, nil
}

func (f *Filesystem) ListUploads(context.Context) ([]UploadInfo, error) { return f.list("staging") }
func (f *Filesystem) ListObjects(context.Context) ([]UploadInfo, error) { return f.list("objects") }

func (f *Filesystem) OpenLocal(_ context.Context, objectKey string) (string, func(), error) {
	if err := checkKey(objectKey); err != nil {
		return "", nil, err
	}
	path := f.objectPath(objectKey)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil, ErrNotFound
	} else if err != nil {
		return "", nil, err
	}
	return path, func() {}, nil
}

func (f *Filesystem) PutObjectFile(_ context.Context, objectKey, sourcePath string) (ObjectInfo, error) {
	if err := checkKey(objectKey); err != nil {
		return ObjectInfo{}, err
	}
	tmp, err := filepath.Rel(filepath.Join(f.root, "tmp"), sourcePath)
	if err != nil || strings.HasPrefix(tmp, "..") || filepath.IsAbs(tmp) {
		return ObjectInfo{}, fmt.Errorf("source must be inside a media work directory")
	}
	source, err := os.OpenFile(sourcePath, os.O_RDWR, 0)
	if err != nil {
		return ObjectInfo{}, err
	}
	syncErr := source.Sync()
	closeErr := source.Close()
	if syncErr != nil {
		return ObjectInfo{}, syncErr
	}
	if closeErr != nil {
		return ObjectInfo{}, closeErr
	}
	final := f.objectPath(objectKey)
	if err := os.Rename(sourcePath, final); err != nil {
		return ObjectInfo{}, err
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return ObjectInfo{}, err
	}
	stat, err := os.Stat(final)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: stat.Size(), ModTime: stat.ModTime()}, nil
}

func (f *Filesystem) WorkDir(context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Join(f.root, "tmp"), "work-*")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func (f *Filesystem) RemoveStaleTemp(_ context.Context, before time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(f.root, "tmp"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(before) {
			continue
		}
		if os.RemoveAll(filepath.Join(f.root, "tmp", entry.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

func hashFile(path string) (ChunkInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return ChunkInfo{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return ChunkInfo{}, err
	}
	info := ChunkInfo{Size: size}
	copy(info.SHA256[:], hash.Sum(nil))
	return info, nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// contextReader stops a long copy when the request is cancelled.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
