package repository

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/storage"
)

func TestRepositoryDuplicatesAndRecoversIndexes(t *testing.T) {
	root := t.TempDir()
	store, err := storage.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := New(store)
	task := testTask(t)
	catalog, err := repo.Initialize(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.TaskID != task.ID {
		t.Fatal("catalog task id mismatch")
	}
	discovered, err := repo.Discover(context.Background())
	if err != nil || len(discovered) != 1 || discovered[0].TaskID != task.ID {
		t.Fatalf("unexpected discovery: %+v %v", discovered, err)
	}
	descriptor, loaded, err := repo.LoadCatalog(context.Background(), task.Name, task.Password)
	if err != nil || descriptor.TaskID != task.ID || loaded.TaskID != task.ID {
		t.Fatalf("load failed: %+v %+v %v", descriptor, loaded, err)
	}
	if err := os.WriteFile(filepath.Join(root, task.Name, "catalog-a.wci"), []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.LoadCatalog(context.Background(), task.Name, task.Password); err != nil {
		t.Fatalf("backup index did not recover: %v", err)
	}
	if _, _, err := repo.LoadCatalog(context.Background(), task.Name, "wrong"); err == nil {
		t.Fatal("wrong password unexpectedly opened catalog")
	}
}

func TestRepositorySnapshotLockAndQuickCheck(t *testing.T) {
	root := t.TempDir()
	store, _ := storage.NewFileStore(root)
	repo := New(store)
	task := testTask(t)
	if _, err := repo.Initialize(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot{ID: "snap", TaskID: task.ID, CreatedAt: time.Now().UTC(), Complete: false, MissingFiles: []string{"a"}}
	if err := repo.SaveSnapshot(context.Background(), task, snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadSnapshot(context.Background(), task.Name, snapshot.ID, task.Password)
	if err != nil || loaded.ID != snapshot.ID {
		t.Fatalf("load snapshot: %+v %v", loaded, err)
	}
	if err := repo.LockSnapshot(context.Background(), task, snapshot.ID, "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, task.Name, "snapshots", snapshot.ID, "KEEP")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UnlockSnapshot(context.Background(), task, snapshot.ID); err != nil {
		t.Fatal(err)
	}
}

func TestQuickCheckReportsAndDeletesUnreferencedObjects(t *testing.T) {
	root := t.TempDir()
	store, _ := storage.NewFileStore(root)
	repo := New(store)
	task := testTask(t)
	if _, err := repo.Initialize(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	content := []byte("orphan")
	orphan := "objects/aa/group/part-00001-orphan.wcb"
	if err := store.Put(context.Background(), task.Name+"/"+orphan, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	check := repo.QuickCheckReferenced(context.Background(), task.Name, nil, nil)
	if len(check.Issues) != 1 || check.Issues[0].Kind != "unreferenced" || check.Issues[0].Path != orphan {
		t.Fatalf("orphan was not reported: %+v", check)
	}
	if issues := repo.DeletePaths(context.Background(), task.Name, []string{orphan}); len(issues) != 0 {
		t.Fatalf("orphan deletion failed: %+v", issues)
	}
	if check := repo.QuickCheckReferenced(context.Background(), task.Name, nil, nil); len(check.Issues) != 0 {
		t.Fatalf("orphan still reported after deletion: %+v", check)
	}
}

func testTask(t *testing.T) model.Task {
	t.Helper()
	salt, err := cryptox.RandomSalt()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return model.Task{ID: "task-id", Name: "测试任务", Mode: model.TaskModeSnapshot, Password: "password", Salt: cryptox.EncodeSalt(salt), BlockSize: 1_000_000_000, Retention: 3, Schedule: model.Schedule{Type: model.ScheduleManual}, CreatedAt: now, UpdatedAt: now}
}
