package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/api"
	"github.com/ccawmiku/webdav-cold-backup/internal/service"
	"github.com/ccawmiku/webdav-cold-backup/internal/testutil"
)

func TestServerRuntimeTaskCreationAndSecretRedaction(t *testing.T) {
	remote := testutil.NewWebDAVServer(t)
	source := t.TempDir()
	filePath := filepath.Join(source, "photo.jpg")
	if err := os.WriteFile(filePath, []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filePath, old, old)
	app, err := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: source, RestoreRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := api.NewServer(app, source, t.TempDir())

	runtimeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(runtimeRecorder, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	if runtimeRecorder.Code != http.StatusOK || !strings.Contains(runtimeRecorder.Body.String(), `"mode":"server"`) {
		t.Fatalf("unexpected runtime response: %d %s", runtimeRecorder.Code, runtimeRecorder.Body.String())
	}

	payload := map[string]any{
		"name": "api-task", "mode": "snapshot", "password": "task-secret",
		"sources":   []map[string]string{{"path": source, "alias": "photos"}},
		"remote":    map[string]string{"endpoint": remote.URL, "root": "api-root", "username": remote.Username, "password": remote.Password},
		"blockSize": 1_000_000_000, "retention": 3,
		"schedule": map[string]any{"type": "manual", "hour": 0, "minute": 0},
	}
	encoded, _ := json.Marshal(payload)
	createRecorder := httptest.NewRecorder()
	handler.ServeHTTP(createRecorder, httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(encoded)))
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("task creation failed: %d %s", createRecorder.Code, createRecorder.Body.String())
	}
	if strings.Contains(createRecorder.Body.String(), "task-secret") || strings.Contains(createRecorder.Body.String(), remote.Password) {
		t.Fatal("API response leaked a password")
	}
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), "api-task") {
		t.Fatalf("task list failed: %d %s", listRecorder.Code, listRecorder.Body.String())
	}
}

func TestServerRejectsUnknownJSONFields(t *testing.T) {
	app, _ := service.New(service.Config{ConfigDir: t.TempDir(), CacheDir: t.TempDir(), SourceRoot: t.TempDir(), RestoreRoot: t.TempDir()})
	defer app.Close()
	handler := api.NewServer(app, t.TempDir(), t.TempDir())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"uploadConcurrency":1,"uploadLimitMiB":0,"downloadLimitMiB":0,"timezone":"Asia/Singapore","unexpected":true}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field unexpectedly accepted: %d", recorder.Code)
	}
}
