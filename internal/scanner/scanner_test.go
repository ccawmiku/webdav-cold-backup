package scanner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
)

func TestScanExcludesSystemFoldersSymlinksAndUnstableFiles(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-time.Hour)
	writeOld := func(relative string) {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(relative), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	writeOld("photos/kept.jpg")
	writeOld("#recycle/deleted.jpg")
	if err := os.WriteFile(filepath.Join(root, "new.jpg"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Symlink(filepath.Join(root, "photos"), filepath.Join(root, "linked"))
	}
	result, err := Scan(context.Background(), []model.SourceRoot{{Path: root, Alias: "root"}}, 10*time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].RelativePath != "photos/kept.jpg" {
		t.Fatalf("unexpected files: %#v", result.Files)
	}
	if result.IgnoredSystem != 1 || result.IgnoredUnstable != 1 || len(result.UnstableFiles) != 1 {
		t.Fatalf("unexpected ignore counts: %+v", result)
	}
}

func TestValidateSourcesRejectsNestedAndDuplicateAliases(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSources([]model.SourceRoot{{Path: root, Alias: "one"}, {Path: child, Alias: "two"}}); err == nil {
		t.Fatal("nested sources unexpectedly accepted")
	}
	other := t.TempDir()
	if err := ValidateSources([]model.SourceRoot{{Path: root, Alias: "same"}, {Path: other, Alias: "SAME"}}); err == nil {
		t.Fatal("duplicate aliases unexpectedly accepted")
	}
}
