package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ccawmiku/webdav-cold-backup/internal/testutil"
	"golang.org/x/net/webdav"
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

func TestWebDAVMoveFallsBackWhenOverwriteIsRejected(t *testing.T) {
	fileSystem := webdav.NewMemFS()
	handler := &webdav.Handler{Prefix: "/", FileSystem: fileSystem, LockSystem: webdav.NewMemLS()}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == "MOVE" {
			destination, err := url.Parse(request.Header.Get("Destination"))
			if err == nil {
				if _, statErr := fileSystem.Stat(request.Context(), strings.TrimPrefix(destination.Path, "/")); statErr == nil {
					writer.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
			}
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)

	store, err := NewWebDAVStore(server.URL, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	oldContent := []byte("old")
	newContent := []byte("new encrypted index")
	if err := store.Put(ctx, "catalog-a.wci", bytes.NewReader(oldContent), int64(len(oldContent))); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "catalog-a.wci.partial-test", bytes.NewReader(newContent), int64(len(newContent))); err != nil {
		t.Fatal(err)
	}
	if err := store.Move(ctx, "catalog-a.wci.partial-test", "catalog-a.wci"); err != nil {
		t.Fatalf("overwrite fallback failed: %v", err)
	}
	reader, err := store.Open(ctx, "catalog-a.wci")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(got, newContent) {
		t.Fatalf("replacement content mismatch: got %q", got)
	}
	if _, err := store.Stat(ctx, "catalog-a.wci.partial-test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("temporary object still exists: %v", err)
	}

	if err := store.MkdirAll(ctx, "source-directory"); err != nil {
		t.Fatal(err)
	}
	if err := store.MkdirAll(ctx, "destination-directory"); err != nil {
		t.Fatal(err)
	}
	keep := []byte("keep")
	if err := store.Put(ctx, "destination-directory/keep.bin", bytes.NewReader(keep), int64(len(keep))); err != nil {
		t.Fatal(err)
	}
	if err := store.Move(ctx, "source-directory", "destination-directory"); err == nil {
		t.Fatal("directory overwrite unexpectedly used the file replacement fallback")
	}
	if _, err := store.Stat(ctx, "destination-directory/keep.bin"); err != nil {
		t.Fatalf("existing destination directory was modified: %v", err)
	}
}

func TestWebDAVRejectsBadCredentials(t *testing.T) {
	server := testutil.NewWebDAVServer(t)
	store, _ := NewWebDAVStore(server.URL, "root", server.Username, "wrong", nil)
	if err := store.TestCompatibility(context.Background()); err == nil {
		t.Fatal("bad credentials unexpectedly passed")
	}
}
