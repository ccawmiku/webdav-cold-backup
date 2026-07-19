package state

import (
	"context"
	"testing"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
)

func TestSQLitePersistsTasksSnapshotsRunsAndSettings(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	task := model.Task{ID: "task", Name: "name", Password: "plain-local-password", CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Task(ctx, task.ID)
	if err != nil || loaded.Password != task.Password {
		t.Fatalf("task mismatch: %+v %v", loaded, err)
	}
	snapshot := model.Snapshot{ID: "snapshot", TaskID: task.ID, CreatedAt: now, Complete: true}
	if err := store.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	run := model.RunRecord{ID: "run", TaskID: task.ID, Status: model.RunComplete, StartedAt: now}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	settings := model.GlobalSettings{UploadConcurrency: 3, UploadLimitMiB: 20, DownloadLimitMiB: 30, Timezone: "Asia/Singapore"}
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	loadedSettings, err := store.Settings(ctx)
	if err != nil || loadedSettings != settings {
		t.Fatalf("settings mismatch: %+v %v", loadedSettings, err)
	}
	if snapshots, _ := store.Snapshots(ctx, task.ID); len(snapshots) != 1 {
		t.Fatalf("unexpected snapshots: %+v", snapshots)
	}
	if runs, _ := store.Runs(ctx, task.ID, 100); len(runs) != 1 {
		t.Fatalf("unexpected runs: %+v", runs)
	}
}
