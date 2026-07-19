package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ccawmiku/webdav-cold-backup/internal/testutil"
)

func TestWebDAVCompatibilityAndObjectLifecycle(t *testing.T) {
	server := testutil.NewWebDAVServer(t)
	store, err := NewWebDAVStore(server.URL, "cold-backup", server.Username, server.Password, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.TestCompatibility(ctx); err != nil {
		t.Fatalf("compatibility test failed: %v", err)
	}
	content := []byte("encrypted object")
	if err := store.Put(ctx, "task/objects/aa/part-00001-test.wcb", bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, "task/objects/aa/part-00001-test.wcb")
	if err != nil || info.Size != int64(len(content)) {
		t.Fatalf("unexpected stat: %+v %v", info, err)
	}
	items, err := store.List(ctx, "task/objects/aa")
	if err != nil || len(items) != 1 {
		t.Fatalf("unexpected list: %+v %v", items, err)
	}
	if err := store.Move(ctx, "task/objects/aa/part-00001-test.wcb", "task/objects/aa/part-00001-moved.wcb"); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(ctx, "task/objects/aa/part-00001-moved.wcb")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(content, got) {
		t.Fatal("download mismatch")
	}
	if err := store.Delete(ctx, "task/objects/aa/part-00001-moved.wcb"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, "task/objects/aa/part-00001-moved.wcb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted object still exists: %v", err)
	}
}

func TestWebDAVRejectsBadCredentials(t *testing.T) {
	server := testutil.NewWebDAVServer(t)
	store, _ := NewWebDAVStore(server.URL, "root", server.Username, "wrong", nil)
	if err := store.TestCompatibility(context.Background()); err == nil {
		t.Fatal("bad credentials unexpectedly passed")
	}
}
