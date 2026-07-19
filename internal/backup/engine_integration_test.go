package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/backup"
	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/offline"
	"github.com/ccawmiku/webdav-cold-backup/internal/repository"
	"github.com/ccawmiku/webdav-cold-backup/internal/restore"
	"github.com/ccawmiku/webdav-cold-backup/internal/state"
	"github.com/ccawmiku/webdav-cold-backup/internal/storage"
)

func TestSnapshotBackupIncrementalMoveCopyRestoreAndSalvage(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	remoteRoot := t.TempDir()
	cacheRoot := t.TempDir()
	configRoot := t.TempDir()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeOld(t, filepath.Join(sourceRoot, "video.mp4"), []byte("test video payload"), old)

	stateStore, err := state.Open(configRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	fileStore, _ := storage.NewFileStore(remoteRoot)
	repo := repository.New(fileStore)
	task := newTask(t, model.TaskModeSnapshot, sourceRoot)
	if err := stateStore.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Initialize(ctx, task); err != nil {
		t.Fatal(err)
	}
	engine := backup.NewEngine(stateStore, cacheRoot, backup.NewControl())
	engine.RetryDelays = []time.Duration{0}
	var lastProgress model.TaskProgress
	engine.Progress = func(progress model.TaskProgress) { lastProgress = progress }
	settings := model.GlobalSettings{UploadConcurrency: 2, Timezone: "Asia/Singapore"}

	firstRun, err := engine.Run(ctx, task, repo, settings)
	if err != nil || firstRun.Status != model.RunComplete || firstRun.FilesAdded != 1 {
		t.Fatalf("first run failed: %+v %v", firstRun, err)
	}
	if lastProgress.Phase != "completed" || lastProgress.Percent != 100 || lastProgress.ObjectsTotal == 0 || lastProgress.ObjectsCompleted != lastProgress.ObjectsTotal {
		t.Fatalf("unexpected completed progress: %+v", lastProgress)
	}
	snapshots, _ := stateStore.Snapshots(ctx, task.ID)
	if len(snapshots) != 1 || len(snapshots[0].Objects) != 1 {
		t.Fatalf("unexpected first snapshot: %+v", snapshots)
	}
	firstSnapshot := snapshots[0]

	secondRun, err := engine.Run(ctx, task, repo, settings)
	if err != nil || secondRun.Message == "" {
		t.Fatalf("no-change run failed: %+v %v", secondRun, err)
	}
	if snapshots, _ := stateStore.Snapshots(ctx, task.ID); len(snapshots) != 1 {
		t.Fatal("no-change run created a snapshot")
	}

	if err := os.Rename(filepath.Join(sourceRoot, "video.mp4"), filepath.Join(sourceRoot, "renamed.mp4")); err != nil {
		t.Fatal(err)
	}
	moveRun, err := engine.Run(ctx, task, repo, settings)
	if err != nil || moveRun.BytesUploaded != 0 {
		t.Fatalf("move should reuse existing object: %+v %v", moveRun, err)
	}
	snapshots, _ = stateStore.Snapshots(ctx, task.ID)
	if len(snapshots) != 2 || snapshots[0].Files[0].RelativePath != "renamed.mp4" || snapshots[0].Objects[0].Path != firstSnapshot.Objects[0].Path {
		t.Fatalf("move was not represented incrementally: %+v", snapshots[0])
	}
	movedOutput := t.TempDir()
	movedReport, err := (&restore.Engine{Repository: repo}).Restore(ctx, task, snapshots[0], nil, movedOutput)
	if err != nil || movedReport.Results[0].Status != restore.StatusRestored {
		t.Fatalf("moved file restore failed: %+v %v", movedReport, err)
	}
	if restored, readErr := os.ReadFile(filepath.Join(movedOutput, "media", "renamed.mp4")); readErr != nil || string(restored) != "test video payload" {
		t.Fatalf("moved file content mismatch: %q %v", restored, readErr)
	}

	content, _ := os.ReadFile(filepath.Join(sourceRoot, "renamed.mp4"))
	writeOld(t, filepath.Join(sourceRoot, "copy.mp4"), content, old)
	copyRun, err := engine.Run(ctx, task, repo, settings)
	if err != nil || copyRun.BytesUploaded == 0 {
		t.Fatalf("copy should be a new file in snapshot mode: %+v %v", copyRun, err)
	}
	snapshots, _ = stateStore.Snapshots(ctx, task.ID)
	if len(snapshots) != 3 || len(snapshots[0].Files) != 2 || len(snapshots[0].Objects) != 2 {
		t.Fatalf("copy snapshot mismatch: %+v", snapshots[0])
	}

	output := t.TempDir()
	restoreEngine := restore.Engine{Repository: repo}
	report, err := restoreEngine.Restore(ctx, task, firstSnapshot, nil, output)
	if err != nil || len(report.Results) != 1 || report.Results[0].Status != restore.StatusRestored {
		t.Fatalf("restore failed: %+v %v", report, err)
	}
	restored, err := os.ReadFile(filepath.Join(output, "media", "video.mp4"))
	if err != nil || string(restored) != "test video payload" {
		t.Fatalf("restored content mismatch: %q %v", restored, err)
	}

	salvagedTask, salvaged, err := offline.Salvage(ctx, filepath.Join(remoteRoot, task.Name), task.Password)
	if err != nil || salvagedTask.ID != task.ID || len(salvaged.Files) < 2 {
		t.Fatalf("self-recovery failed: %+v %+v %v", salvagedTask, salvaged, err)
	}
}

func TestArchiveModeKeepsOnlyFirstDuplicatePath(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeOld(t, filepath.Join(sourceRoot, "a.jpg"), []byte("same content"), old)
	writeOld(t, filepath.Join(sourceRoot, "b.jpg"), []byte("same content"), old)
	stateStore, _ := state.Open(t.TempDir())
	defer stateStore.Close()
	fileStore, _ := storage.NewFileStore(t.TempDir())
	repo := repository.New(fileStore)
	task := newTask(t, model.TaskModeArchive, sourceRoot)
	_ = stateStore.SaveTask(ctx, task)
	_, _ = repo.Initialize(ctx, task)
	engine := backup.NewEngine(stateStore, t.TempDir(), backup.NewControl())
	engine.RetryDelays = []time.Duration{0}
	run, err := engine.Run(ctx, task, repo, model.GlobalSettings{UploadConcurrency: 1, Timezone: "Asia/Singapore"})
	if err != nil || run.Status != model.RunComplete {
		t.Fatalf("archive run failed: %+v %v", run, err)
	}
	archive, err := stateStore.Snapshot(ctx, task.ID, "archive")
	if err != nil || len(archive.Files) != 1 || archive.Files[0].RelativePath != "a.jpg" {
		t.Fatalf("archive duplicate semantics mismatch: %+v %v", archive, err)
	}
}

func newTask(t *testing.T, mode model.TaskMode, sourceRoot string) model.Task {
	t.Helper()
	salt, err := cryptox.RandomSalt()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return model.Task{
		ID: "task-" + string(mode), Name: "任务-" + string(mode), Mode: mode,
		Password: "test password", Salt: cryptox.EncodeSalt(salt),
		Sources:   []model.SourceRoot{{Path: sourceRoot, Alias: "media"}},
		BlockSize: 40_000_000, Retention: 3, Schedule: model.Schedule{Type: model.ScheduleManual},
		Status: model.TaskIdle, CreatedAt: now, UpdatedAt: now, AttachedWritable: true,
	}
}

func writeOld(t *testing.T, path string, content []byte, timestamp time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
}
