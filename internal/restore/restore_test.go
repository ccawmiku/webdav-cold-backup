package restore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, "media", "../../outside.txt"); err == nil {
		t.Fatal("path traversal unexpectedly accepted")
	}
	path, err := safeJoin(root, "media", "folder/file.txt")
	if err != nil || path != filepath.Join(root, "media", "folder", "file.txt") {
		t.Fatalf("safe path mismatch: %s %v", path, err)
	}
}

func TestWindowsNameValidation(t *testing.T) {
	if !validWindowsPath("media", "folder/file.mp4") {
		t.Fatal("valid Windows path rejected")
	}
	for _, path := range []string{"CON.txt", "folder/name?.jpg", "folder/trailing. ", "AUX"} {
		if validWindowsPath("media", path) {
			t.Fatalf("invalid Windows path accepted: %s", path)
		}
	}
}

func TestChooseTargetUsesConflictDirectory(t *testing.T) {
	root := t.TempDir()
	entry := model.FileEntry{RootAlias: "media", RelativePath: "file.txt", Hash: "not-the-existing-hash"}
	target := filepath.Join(root, "media", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	chosen, conflict, err := chooseTarget(root, entry, map[string]string{})
	if err != nil || conflict != existingDifferent {
		t.Fatalf("expected conflict: %s %v %v", chosen, conflict, err)
	}
	if chosen != filepath.Join(root, "冲突文件", "media", "file.txt") {
		t.Fatalf("unexpected conflict path: %s", chosen)
	}
}
