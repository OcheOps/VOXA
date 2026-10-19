package mediastore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OcheOps/voxa/server/internal/mediastore"
)

const upload = "med_0123456789abcdef0123456789abcdef"

func newStore(t *testing.T) (*mediastore.Filesystem, string) {
	t.Helper()
	root := t.TempDir()
	store, err := mediastore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func put(t *testing.T, store mediastore.Store, index int, data string) {
	t.Helper()
	if _, err := store.PutChunk(context.Background(), upload, index, strings.NewReader(data), 1024); err != nil {
		t.Fatalf("put %d: %v", index, err)
	}
}

func TestChunksAssembleInOrder(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	put(t, store, 1, "world")
	put(t, store, 0, "hello ")
	chunks, err := store.ListChunks(ctx, upload)
	if err != nil || len(chunks) != 2 || chunks[0].Index != 0 || chunks[1].Size != 5 {
		t.Fatalf("chunks=%#v err=%v", chunks, err)
	}
	info, err := store.Assemble(ctx, upload, 2, upload)
	if err != nil || info.Size != 11 {
		t.Fatalf("assemble info=%#v err=%v", info, err)
	}
	object, err := store.Open(ctx, upload)
	if err != nil {
		t.Fatal(err)
	}
	defer object.Close()
	content, _ := io.ReadAll(object)
	if string(content) != "hello world" || object.Info().Size != 11 {
		t.Fatalf("content=%q", content)
	}
	// Assembly is retryable and leaves no temporary files behind.
	if _, err := store.Assemble(ctx, upload, 2, upload); err != nil {
		t.Fatal(err)
	}
	if leftovers, _ := os.ReadDir(filepath.Join(root, "tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func TestDuplicateChunksAreIdempotentAndConflictsDetected(t *testing.T) {
	store, _ := newStore(t)
	put(t, store, 0, "same")
	put(t, store, 0, "same")
	if _, err := store.PutChunk(context.Background(), upload, 0, strings.NewReader("different"), 1024); !errors.Is(err, mediastore.ErrChunkConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	chunks, _ := store.ListChunks(context.Background(), upload)
	if len(chunks) != 1 || chunks[0].Size != 4 {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestChunkSizeLimitAndMissingChunks(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	if _, err := store.PutChunk(ctx, upload, 0, bytes.NewReader(make([]byte, 11)), 10); !errors.Is(err, mediastore.ErrTooLarge) {
		t.Fatalf("oversized err = %v", err)
	}
	if chunks, _ := store.ListChunks(ctx, upload); len(chunks) != 0 {
		t.Fatalf("oversized chunk stored: %#v", chunks)
	}
	put(t, store, 0, "a")
	put(t, store, 2, "c")
	if _, err := store.Assemble(ctx, upload, 3, upload); !errors.Is(err, mediastore.ErrMissingChunk) {
		t.Fatalf("missing chunk err = %v", err)
	}
	if _, err := store.Open(ctx, upload); !errors.Is(err, mediastore.ErrNotFound) {
		t.Fatalf("object created despite missing chunk: %v", err)
	}
}

func TestKeysCannotEscapeTheMediaDirectory(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	for _, key := range []string{"../etc", "med_../../x", "/abs", "med_0123", "MED_0123456789abcdef0123456789abcdef", "med_0123456789abcdef0123456789abcdef/../x", ""} {
		if _, err := store.PutChunk(ctx, key, 0, strings.NewReader("x"), 10); !errors.Is(err, mediastore.ErrInvalidKey) {
			t.Errorf("PutChunk(%q) err = %v", key, err)
		}
		if _, err := store.Open(ctx, key); !errors.Is(err, mediastore.ErrInvalidKey) {
			t.Errorf("Open(%q) err = %v", key, err)
		}
		if err := store.DeleteUpload(ctx, key); !errors.Is(err, mediastore.ErrInvalidKey) {
			t.Errorf("DeleteUpload(%q) err = %v", key, err)
		}
	}
	if _, err := store.PutChunk(ctx, upload, -1, strings.NewReader("x"), 10); err == nil {
		t.Error("negative index accepted")
	}
	if entries, _ := os.ReadDir(filepath.Dir(root)); len(entries) != 1 {
		t.Fatalf("files written outside the media root: %v", entries)
	}
}

func TestStateSurvivesRestartAndDeletes(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	put(t, store, 0, "persisted")
	restarted, err := mediastore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if chunks, _ := restarted.ListChunks(ctx, upload); len(chunks) != 1 {
		t.Fatalf("chunks after restart = %#v", chunks)
	}
	uploads, _ := restarted.ListUploads(ctx)
	if len(uploads) != 1 || uploads[0].Key != upload {
		t.Fatalf("uploads = %#v", uploads)
	}
	if _, err := restarted.Assemble(ctx, upload, 1, upload); err != nil {
		t.Fatal(err)
	}
	if objects, _ := restarted.ListObjects(ctx); len(objects) != 1 {
		t.Fatalf("objects = %#v", objects)
	}
	if err := restarted.DeleteUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	if err := restarted.DeleteObject(ctx, upload); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Open(ctx, upload); !errors.Is(err, mediastore.ErrNotFound) {
		t.Fatalf("open after delete err = %v", err)
	}
	if err := restarted.DeleteObject(ctx, upload); err != nil {
		t.Fatalf("deleting a missing object should be a no-op: %v", err)
	}
}
