package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestFileStoreRoundTripAndRootBoundary(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	content := []byte("hello")
	if err := store.Put(ctx, "a/b.txt", bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, "a/b.txt")
	if err != nil || info.Size != int64(len(content)) {
		t.Fatalf("unexpected stat: %+v %v", info, err)
	}
	reader, err := store.Open(ctx, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(content, restored) {
		t.Fatal("content mismatch")
	}
	if err := store.Move(ctx, "a/b.txt", "a/c.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, "a/b.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old path still exists: %v", err)
	}
	if _, err := store.Stat(ctx, "../../outside"); err == nil {
		t.Fatal("path traversal unexpectedly accepted")
	}
}
