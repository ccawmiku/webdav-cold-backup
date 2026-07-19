package service_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/restore"
	"github.com/ccawmiku/webdav-cold-backup/internal/service"
	"github.com/ccawmiku/webdav-cold-backup/internal/storage"
	"github.com/ccawmiku/webdav-cold-backup/internal/testutil"
)

func TestZeroStateAttachRebuildsWritableTaskAndDetectsSourceMismatch(t *testing.T) {
	ctx := context.Background()
	remoteServer := testutil.NewWebDAVServer(t)
	remote := model.WebDAVConfig{Endpoint: remoteServer.URL, Root: "reattach-root", Username: remoteServer.Username, Password: remoteServer.Password}
	source := t.TempDir()
	filePath := filepath.Join(source, "movie.mp4")
	if err := os.WriteFile(filePath, []byte("movie data"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filePath, old, old)

	first, err := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: source, RestoreRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.CreateTask(ctx, service.CreateTaskInput{
		Name: "reattach-task", Mode: model.TaskModeSnapshot, Password: "task password",
		Sources: []model.SourceRoot{{Path: source, Alias: "media"}}, Remote: remote,
		BlockSize: 1_000_000_000, Retention: 3, Schedule: model.Schedule{Type: model.ScheduleManual},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Enqueue(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, first, created.ID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: source, RestoreRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := second.Attach(ctx, service.AttachInput{Remote: remote, TaskName: "reattach-task", Password: "task password", Sources: []model.SourceRoot{{Path: source, Alias: "media"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !attached.Writable || attached.Task.ID != created.ID || attached.Check.Checked == 0 || len(attached.Differences) != 0 {
		t.Fatalf("reattach did not rebuild writable task: %+v", attached)
	}
	_ = second.Close()

	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	third, _ := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: source, RestoreRoot: t.TempDir()})
	defer third.Close()
	mismatch, err := third.Attach(ctx, service.AttachInput{Remote: remote, TaskName: "reattach-task", Password: "task password", Sources: []model.SourceRoot{{Path: source, Alias: "media"}}})
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Writable || len(mismatch.Differences) == 0 {
		t.Fatalf("missing source was not detected: %+v", mismatch)
	}
}

func TestRestoreImportedDownloadedTaskDirectory(t *testing.T) {
	ctx := context.Background()
	remoteServer := testutil.NewWebDAVServer(t)
	remote := model.WebDAVConfig{Endpoint: remoteServer.URL, Root: "import-root", Username: remoteServer.Username, Password: remoteServer.Password}
	source := t.TempDir()
	filePath := filepath.Join(source, "album", "photo.jpg")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("downloaded block restore"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filePath, old, old)

	restoreRoot := t.TempDir()
	app, err := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: source, RestoreRoot: restoreRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	task, err := app.CreateTask(ctx, service.CreateTaskInput{
		Name: "import-task", Mode: model.TaskModeSnapshot, Password: "task password",
		Sources: []model.SourceRoot{{Path: source, Alias: "media"}}, Remote: remote,
		BlockSize: 1_000_000_000, Retention: 3, Schedule: model.Schedule{Type: model.ScheduleManual},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Enqueue(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, app, task.ID)
	snapshots, err := app.Snapshots(ctx, task.ID)
	if err != nil || len(snapshots) != 1 || len(snapshots[0].Objects) == 0 {
		t.Fatalf("unexpected snapshot: %+v %v", snapshots, err)
	}

	webDAV, err := storage.NewWebDAVStore(remote.Endpoint, remote.Root, remote.Username, remote.Password, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	importedRoot := filepath.Join(restoreRoot, "renamed-downloaded-task")
	for _, objectRecord := range snapshots[0].Objects {
		reader, openErr := webDAV.Open(ctx, task.Name+"/"+objectRecord.Path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		destination := filepath.Join(importedRoot, filepath.FromSlash(objectRecord.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		writer, createErr := os.Create(destination)
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, copyErr := io.Copy(writer, reader)
		closeWriteErr := writer.Close()
		closeReadErr := reader.Close()
		if copyErr != nil || closeWriteErr != nil || closeReadErr != nil {
			t.Fatalf("copy downloaded object: %v %v %v", copyErr, closeWriteErr, closeReadErr)
		}
	}

	output := filepath.Join(restoreRoot, "first-output")
	report, err := app.RestoreImported(ctx, task.ID, snapshots[0].ID, nil, importedRoot, output)
	if err != nil || len(report.Results) != 1 || report.Results[0].Status != restore.StatusRestored {
		t.Fatalf("imported restore failed: %+v %v", report, err)
	}
	content, err := os.ReadFile(filepath.Join(output, "media", "album", "photo.jpg"))
	if err != nil || string(content) != "downloaded block restore" {
		t.Fatalf("restored content mismatch: %q %v", content, err)
	}

	if err := os.Remove(filepath.Join(importedRoot, filepath.FromSlash(snapshots[0].Objects[0].Path))); err != nil {
		t.Fatal(err)
	}
	missingReport, err := app.RestoreImported(ctx, task.ID, snapshots[0].ID, nil, importedRoot, filepath.Join(restoreRoot, "missing-output"))
	if err != nil || missingReport.Results[0].Status != restore.StatusMissing {
		t.Fatalf("missing imported object was not reported per file: %+v %v", missingReport, err)
	}
}

func TestServiceRejectsPathsOutsideConfiguredMappings(t *testing.T) {
	sourceRoot := t.TempDir()
	restoreRoot := t.TempDir()
	app, err := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: sourceRoot, RestoreRoot: restoreRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	_, err = app.CreateTask(context.Background(), service.CreateTaskInput{
		Name: "outside", Mode: model.TaskModeSnapshot, Password: "password",
		Sources:   []model.SourceRoot{{Path: t.TempDir(), Alias: "outside"}},
		BlockSize: 1_000_000_000, Retention: 3, Schedule: model.Schedule{Type: model.ScheduleManual},
	})
	if err == nil {
		t.Fatal("source outside configured mapping was accepted")
	}
	if _, err := app.Restore(context.Background(), "missing-task", "", nil, t.TempDir()); err == nil {
		t.Fatal("restore output outside configured mapping was accepted")
	}
}

func waitForRun(t *testing.T, app *service.Service, taskID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		task, err := app.Task(context.Background(), taskID)
		if err == nil && task.LastRunAt != nil && task.Status == model.TaskIdle {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("backup run did not finish")
}
