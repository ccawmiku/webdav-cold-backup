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
	"github.com/ccawmiku/webdav-cold-backup/internal/offline"
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

	presetPayload, _ := json.Marshal(map[string]any{
		"name":   "主网盘",
		"remote": map[string]string{"endpoint": remote.URL, "root": "", "username": remote.Username, "password": remote.Password},
	})
	presetRecorder := httptest.NewRecorder()
	handler.ServeHTTP(presetRecorder, httptest.NewRequest(http.MethodPost, "/api/remote-presets", bytes.NewReader(presetPayload)))
	if presetRecorder.Code != http.StatusCreated || strings.Contains(presetRecorder.Body.String(), remote.Password) {
		t.Fatalf("remote preset failed or leaked password: %d %s", presetRecorder.Code, presetRecorder.Body.String())
	}
	var preset struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(presetRecorder.Body.Bytes(), &preset); err != nil || preset.ID == "" {
		t.Fatalf("invalid preset response: %v %s", err, presetRecorder.Body.String())
	}

	payload := map[string]any{
		"name": "api-task", "mode": "snapshot", "password": "task-secret", "passwordConfirm": "task-secret",
		"sources":        []map[string]string{{"path": source, "alias": "photos"}},
		"remotePresetId": preset.ID,
		"remote":         map[string]string{"endpoint": "", "root": "", "username": "", "password": ""},
		"blockSize":      1_000_000_000, "retention": 3,
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
	browsePayload, _ := json.Marshal(map[string]any{"remotePresetId": preset.ID, "remote": map[string]string{}, "path": ""})
	browseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(browseRecorder, httptest.NewRequest(http.MethodPost, "/api/remotes/browse", bytes.NewReader(browsePayload)))
	if browseRecorder.Code != http.StatusOK || !strings.Contains(browseRecorder.Body.String(), "api-task") {
		t.Fatalf("preset-backed remote browse failed: %d %s", browseRecorder.Code, browseRecorder.Body.String())
	}
	discoverPayload, _ := json.Marshal(map[string]any{"remotePresetId": preset.ID, "remote": map[string]string{}})
	discoverRecorder := httptest.NewRecorder()
	handler.ServeHTTP(discoverRecorder, httptest.NewRequest(http.MethodPost, "/api/remotes/discover", bytes.NewReader(discoverPayload)))
	if discoverRecorder.Code != http.StatusOK || !strings.Contains(discoverRecorder.Body.String(), "api-task") {
		t.Fatalf("preset-backed task discovery failed: %d %s", discoverRecorder.Code, discoverRecorder.Body.String())
	}
	progressRecorder := httptest.NewRecorder()
	handler.ServeHTTP(progressRecorder, httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskIDFromResponse(t, createRecorder.Body.Bytes())+"/progress", nil))
	if progressRecorder.Code != http.StatusOK || !strings.Contains(progressRecorder.Body.String(), `"phase":"idle"`) {
		t.Fatalf("progress response failed: %d %s", progressRecorder.Code, progressRecorder.Body.String())
	}
	mismatchPayload := map[string]any{
		"name": "bad-password-task", "mode": "snapshot", "password": "first", "passwordConfirm": "second",
		"sources": []map[string]string{{"path": source, "alias": "photos"}}, "remotePresetId": preset.ID,
		"remote": map[string]string{}, "blockSize": 1_000_000_000, "retention": 3,
		"schedule": map[string]any{"type": "manual", "hour": 0, "minute": 0},
	}
	mismatchEncoded, _ := json.Marshal(mismatchPayload)
	mismatchRecorder := httptest.NewRecorder()
	handler.ServeHTTP(mismatchRecorder, httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(mismatchEncoded)))
	if mismatchRecorder.Code != http.StatusBadRequest || !strings.Contains(mismatchRecorder.Body.String(), "密码不一致") {
		t.Fatalf("mismatched task passwords were accepted: %d %s", mismatchRecorder.Code, mismatchRecorder.Body.String())
	}
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), "api-task") {
		t.Fatalf("task list failed: %d %s", listRecorder.Code, listRecorder.Body.String())
	}
}

func TestOfflineRestoreProgressStartsIdle(t *testing.T) {
	handler := api.NewOffline(&offline.Session{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/offline/progress", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"idle"`) || !strings.Contains(recorder.Body.String(), `"phase":"idle"`) {
		t.Fatalf("unexpected offline progress response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func taskIDFromResponse(t *testing.T, encoded []byte) string {
	t.Helper()
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(encoded, &task); err != nil || task.ID == "" {
		t.Fatalf("invalid task response: %v %s", err, encoded)
	}
	return task.ID
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
