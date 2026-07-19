package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/offline"
	"github.com/ccawmiku/webdav-cold-backup/internal/service"
	"github.com/ccawmiku/webdav-cold-backup/internal/version"
	"github.com/ccawmiku/webdav-cold-backup/internal/webui"
)

type Mode string

const (
	ModeServer  Mode = "server"
	ModeOffline Mode = "offline"
)

type Server struct {
	mode        Mode
	service     *service.Service
	offline     *offline.Session
	sourceRoot  string
	restoreRoot string
	handler     http.Handler
}

func NewServer(app *service.Service, sourceRoot, restoreRoot string) *Server {
	server := &Server{mode: ModeServer, service: app, sourceRoot: sourceRoot, restoreRoot: restoreRoot}
	server.handler = server.routes()
	return server
}

func NewOffline(session *offline.Session) *Server {
	server := &Server{mode: ModeOffline, offline: session}
	server.handler = server.routes()
	return server
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/runtime", s.runtime)
	mux.HandleFunc("GET /api/fs", s.fileSystem)
	if s.mode == ModeServer {
		mux.HandleFunc("GET /api/tasks", s.tasks)
		mux.HandleFunc("POST /api/tasks", s.createTask)
		mux.HandleFunc("GET /api/tasks/{id}", s.task)
		mux.HandleFunc("PUT /api/tasks/{id}", s.updateTask)
		mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
		mux.HandleFunc("POST /api/tasks/{id}/run", s.runTask)
		mux.HandleFunc("POST /api/tasks/{id}/pause", s.pauseTask)
		mux.HandleFunc("POST /api/tasks/{id}/resume", s.resumeTask)
		mux.HandleFunc("POST /api/tasks/{id}/reconnect", s.reconnect)
		mux.HandleFunc("GET /api/tasks/{id}/snapshots", s.snapshots)
		mux.HandleFunc("GET /api/tasks/{id}/files", s.files)
		mux.HandleFunc("GET /api/tasks/{id}/runs", s.runs)
		mux.HandleFunc("POST /api/tasks/{id}/check", s.quickCheck)
		mux.HandleFunc("POST /api/tasks/{id}/cleanup", s.cleanup)
		mux.HandleFunc("POST /api/tasks/{id}/restore", s.restoreTask)
		mux.HandleFunc("POST /api/tasks/{id}/restore-imported", s.restoreImported)
		mux.HandleFunc("POST /api/tasks/{id}/plan", s.plan)
		mux.HandleFunc("POST /api/tasks/{id}/archive-delete", s.archiveDelete)
		mux.HandleFunc("POST /api/tasks/{id}/snapshots/{snapshot}/lock", s.lockSnapshot)
		mux.HandleFunc("DELETE /api/tasks/{id}/snapshots/{snapshot}", s.deleteSnapshot)
		mux.HandleFunc("POST /api/remotes/discover", s.discover)
		mux.HandleFunc("POST /api/remotes/attach", s.attach)
		mux.HandleFunc("GET /api/settings", s.settings)
		mux.HandleFunc("PUT /api/settings", s.saveSettings)
	} else {
		mux.HandleFunc("POST /api/offline/open", s.offlineOpen)
		mux.HandleFunc("POST /api/offline/select", s.offlineSelect)
		mux.HandleFunc("GET /api/offline/files", s.offlineFiles)
		mux.HandleFunc("POST /api/offline/restore", s.offlineRestore)
	}
	mux.Handle("/", webui.Handler())
	return recoverMiddleware(loggingHeaders(mux))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "mode": s.mode, "version": version.Version})
}

func (s *Server) runtime(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"mode": s.mode, "version": version.Version, "platform": runtime.GOOS + "/" + runtime.GOARCH})
}

func (s *Server) tasks(writer http.ResponseWriter, request *http.Request) {
	items, err := s.service.Tasks(request.Context())
	respond(writer, items, err)
}

func (s *Server) createTask(writer http.ResponseWriter, request *http.Request) {
	var input service.CreateTaskInput
	if !decode(writer, request, &input) {
		return
	}
	task, err := s.service.CreateTask(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, task)
}

func (s *Server) task(writer http.ResponseWriter, request *http.Request) {
	task, err := s.service.Task(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, task.Public())
}

func (s *Server) updateTask(writer http.ResponseWriter, request *http.Request) {
	var input service.UpdateTaskInput
	if !decode(writer, request, &input) {
		return
	}
	task, err := s.service.UpdateTask(request.Context(), request.PathValue("id"), input)
	respond(writer, task, err)
}

func (s *Server) deleteTask(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password    string `json:"password"`
		ConfirmName string `json:"confirmName"`
	}
	if !decode(writer, request, &input) {
		return
	}
	if err := s.service.DeleteTask(request.Context(), request.PathValue("id"), input.Password, input.ConfirmName); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) runTask(writer http.ResponseWriter, request *http.Request) {
	err := s.service.Enqueue(request.Context(), request.PathValue("id"))
	respondAction(writer, err)
}

func (s *Server) pauseTask(writer http.ResponseWriter, request *http.Request) {
	respondAction(writer, s.service.Pause(request.Context(), request.PathValue("id")))
}

func (s *Server) resumeTask(writer http.ResponseWriter, request *http.Request) {
	respondAction(writer, s.service.Resume(request.Context(), request.PathValue("id")))
}

func (s *Server) reconnect(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Remote       model.WebDAVConfig `json:"remote"`
		ConfirmWrite bool               `json:"confirmWrite"`
	}
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.Reconnect(request.Context(), request.PathValue("id"), input.Remote, input.ConfirmWrite)
	respond(writer, result, err)
}

func (s *Server) snapshots(writer http.ResponseWriter, request *http.Request) {
	items, err := s.service.Snapshots(request.Context(), request.PathValue("id"))
	respond(writer, items, err)
}

func (s *Server) files(writer http.ResponseWriter, request *http.Request) {
	task, err := s.service.Task(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	snapshotID := request.URL.Query().Get("snapshot")
	var snapshot model.Snapshot
	if task.Mode == model.TaskModeArchive {
		snapshot, err = s.service.State().Snapshot(request.Context(), task.ID, "archive")
	} else if snapshotID != "" {
		snapshot, err = s.service.State().Snapshot(request.Context(), task.ID, snapshotID)
	} else {
		items, listErr := s.service.Snapshots(request.Context(), task.ID)
		err = listErr
		if err == nil && len(items) > 0 {
			snapshot = items[0]
		}
	}
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot.Files)
}

func (s *Server) runs(writer http.ResponseWriter, request *http.Request) {
	items, err := s.service.Runs(request.Context(), request.PathValue("id"))
	respond(writer, items, err)
}

func (s *Server) quickCheck(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SnapshotID string `json:"snapshotId"`
	}
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.QuickCheck(request.Context(), request.PathValue("id"), input.SnapshotID)
	respond(writer, result, err)
}

func (s *Server) cleanup(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decode(writer, request, &input) {
		return
	}
	deleted, err := s.service.CleanupUnreferenced(request.Context(), request.PathValue("id"), input.Password)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]int{"deleted": deleted})
}

type restoreRequest struct {
	SnapshotID string   `json:"snapshotId"`
	Selected   []string `json:"selected"`
	Output     string   `json:"output"`
}

type importedRestoreRequest struct {
	SnapshotID    string   `json:"snapshotId"`
	Selected      []string `json:"selected"`
	TaskDirectory string   `json:"taskDirectory"`
	Output        string   `json:"output"`
}

func (s *Server) restoreTask(writer http.ResponseWriter, request *http.Request) {
	var input restoreRequest
	if !decode(writer, request, &input) {
		return
	}
	if !withinRoots(input.Output, []string{s.restoreRoot}) {
		writeError(writer, http.StatusBadRequest, errors.New("output directory must be within the configured restore root"))
		return
	}
	report, err := s.service.Restore(request.Context(), request.PathValue("id"), input.SnapshotID, input.Selected, input.Output)
	respond(writer, report, err)
}

func (s *Server) restoreImported(writer http.ResponseWriter, request *http.Request) {
	var input importedRestoreRequest
	if !decode(writer, request, &input) {
		return
	}
	if !withinRoots(input.TaskDirectory, []string{s.sourceRoot, s.restoreRoot}) || !withinRoots(input.Output, []string{s.restoreRoot}) {
		writeError(writer, http.StatusBadRequest, errors.New("import and output directories must be within configured roots"))
		return
	}
	report, err := s.service.RestoreImported(request.Context(), request.PathValue("id"), input.SnapshotID, input.Selected, input.TaskDirectory, input.Output)
	respond(writer, report, err)
}

func (s *Server) plan(writer http.ResponseWriter, request *http.Request) {
	var input restoreRequest
	if !decode(writer, request, &input) {
		return
	}
	plan, err := s.service.BuildPlan(request.Context(), request.PathValue("id"), input.SnapshotID, input.Selected)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Disposition", `attachment; filename="restore.backup-plan"`)
	_ = json.NewEncoder(writer).Encode(plan)
}

func (s *Server) archiveDelete(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password string   `json:"password"`
		FileIDs  []string `json:"fileIds"`
	}
	if !decode(writer, request, &input) {
		return
	}
	respondAction(writer, s.service.DeleteArchiveFiles(request.Context(), request.PathValue("id"), input.Password, input.FileIDs))
}

func (s *Server) lockSnapshot(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password string `json:"password"`
		Note     string `json:"note"`
		Locked   bool   `json:"locked"`
	}
	if !decode(writer, request, &input) {
		return
	}
	err := s.service.LockSnapshot(request.Context(), request.PathValue("id"), request.PathValue("snapshot"), input.Password, input.Note, input.Locked)
	respondAction(writer, err)
}

func (s *Server) deleteSnapshot(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decode(writer, request, &input) {
		return
	}
	err := s.service.DeleteSnapshot(request.Context(), request.PathValue("id"), request.PathValue("snapshot"), input.Password)
	respondAction(writer, err)
}

func (s *Server) discover(writer http.ResponseWriter, request *http.Request) {
	var remote model.WebDAVConfig
	if !decode(writer, request, &remote) {
		return
	}
	items, err := s.service.Discover(request.Context(), remote)
	respond(writer, items, err)
}

func (s *Server) attach(writer http.ResponseWriter, request *http.Request) {
	var input service.AttachInput
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.Attach(request.Context(), input)
	respond(writer, result, err)
}

func (s *Server) settings(writer http.ResponseWriter, request *http.Request) {
	settings, err := s.service.Settings(request.Context())
	respond(writer, settings, err)
}

func (s *Server) saveSettings(writer http.ResponseWriter, request *http.Request) {
	var settings model.GlobalSettings
	if !decode(writer, request, &settings) {
		return
	}
	if err := s.service.SaveSettings(request.Context(), settings); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, settings)
}

func (s *Server) offlineOpen(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Directory string `json:"directory"`
		Password  string `json:"password"`
	}
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.offline.Open(request.Context(), input.Directory, input.Password)
	respond(writer, result, err)
}

func (s *Server) offlineSelect(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SnapshotID string `json:"snapshotId"`
	}
	if !decode(writer, request, &input) {
		return
	}
	respondAction(writer, s.offline.Select(input.SnapshotID))
}

func (s *Server) offlineFiles(writer http.ResponseWriter, _ *http.Request) {
	files, err := s.offline.Files()
	respond(writer, files, err)
}

func (s *Server) offlineRestore(writer http.ResponseWriter, request *http.Request) {
	var input restoreRequest
	if !decode(writer, request, &input) {
		return
	}
	report, err := s.offline.Restore(request.Context(), input.Selected, input.Output)
	respond(writer, report, err)
}

type fsItem struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func (s *Server) fileSystem(writer http.ResponseWriter, request *http.Request) {
	requested := request.URL.Query().Get("path")
	if requested == "" {
		writeJSON(writer, http.StatusOK, s.rootDirectories())
		return
	}
	if s.mode == ModeServer && !withinRoots(requested, []string{s.sourceRoot, s.restoreRoot}) {
		writeError(writer, http.StatusForbidden, errors.New("目录不在已映射范围内"))
		return
	}
	entries, err := os.ReadDir(requested)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	items := []fsItem{}
	for _, entry := range entries {
		if entry.IsDir() {
			items = append(items, fsItem{Path: filepath.Join(requested, entry.Name()), Name: entry.Name()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	writeJSON(writer, http.StatusOK, items)
}

func (s *Server) rootDirectories() []fsItem {
	if s.mode == ModeServer {
		items := []fsItem{}
		for _, root := range []string{s.sourceRoot, s.restoreRoot} {
			if root != "" {
				items = append(items, fsItem{Path: root, Name: filepath.Base(root)})
			}
		}
		return items
	}
	if runtime.GOOS == "windows" {
		items := []fsItem{}
		for letter := 'A'; letter <= 'Z'; letter++ {
			root := string(letter) + `:\`
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				items = append(items, fsItem{Path: root, Name: root})
			}
		}
		return items
	}
	return []fsItem{{Path: "/", Name: "/"}}
}

func withinRoots(requested string, roots []string) bool {
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(absoluteRoot, absolute)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func decode(writer http.ResponseWriter, request *http.Request, destination any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 4*1024*1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("请求格式无效: %w", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, errors.New("请求只能包含一个JSON对象"))
		return false
	}
	return true
}

func respond(writer http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func respondAction(writer http.ResponseWriter, err error) {
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func loggingHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(writer, request)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeError(writer, http.StatusInternalServerError, errors.New("服务器内部错误"))
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func ParseInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

var _ = context.Canceled
var _ = time.Second
