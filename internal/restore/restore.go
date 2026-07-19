package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/fsmeta"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/repository"
)

type FileStatus string

const (
	StatusReady    FileStatus = "ready"
	StatusRestored FileStatus = "restored"
	StatusSkipped  FileStatus = "skipped"
	StatusConflict FileStatus = "conflict"
	StatusMissing  FileStatus = "missing"
	StatusInvalid  FileStatus = "invalid"
	StatusFailed   FileStatus = "failed"
)

type FileResult struct {
	RootAlias    string     `json:"rootAlias"`
	RelativePath string     `json:"relativePath"`
	OutputPath   string     `json:"outputPath,omitempty"`
	Status       FileStatus `json:"status"`
	Message      string     `json:"message,omitempty"`
}

type Report struct {
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt"`
	SnapshotID string       `json:"snapshotId"`
	Results    []FileResult `json:"results"`
}

type Plan struct {
	FormatVersion int          `json:"formatVersion"`
	TaskID        string       `json:"taskId"`
	TaskName      string       `json:"taskName"`
	SnapshotID    string       `json:"snapshotId"`
	CreatedAt     time.Time    `json:"createdAt"`
	Files         []string     `json:"files"`
	Objects       []PlanObject `json:"objects"`
}

type PlanObject struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

type Engine struct {
	Repository *repository.Repository
}

func (e *Engine) BuildPlan(task model.Task, snapshot model.Snapshot, selected []string) Plan {
	selection := selectionMap(selected)
	objects := map[string]PlanObject{}
	files := []string{}
	for _, file := range snapshot.Files {
		key := fileKey(file.RootAlias, file.RelativePath)
		if !selectedFile(selection, key) {
			continue
		}
		files = append(files, key)
		for _, block := range file.Blocks {
			objects[block.ObjectPath] = PlanObject{Path: block.ObjectPath, Size: block.ObjectSize, Hash: block.ObjectHash}
		}
	}
	objectList := make([]PlanObject, 0, len(objects))
	for _, object := range objects {
		objectList = append(objectList, object)
	}
	sort.Slice(objectList, func(i, j int) bool { return objectList[i].Path < objectList[j].Path })
	sort.Strings(files)
	return Plan{FormatVersion: model.FormatVersion, TaskID: task.ID, TaskName: task.Name, SnapshotID: snapshot.ID, CreatedAt: time.Now().UTC(), Files: files, Objects: objectList}
}

func (e *Engine) Preflight(ctx context.Context, task model.Task, snapshot model.Snapshot, selected []string) []FileResult {
	selection := selectionMap(selected)
	results := []FileResult{}
	objectStatus := map[string]error{}
	for _, file := range snapshot.Files {
		key := fileKey(file.RootAlias, file.RelativePath)
		if !selectedFile(selection, key) {
			continue
		}
		result := FileResult{RootAlias: file.RootAlias, RelativePath: file.RelativePath, Status: StatusReady}
		if file.MissingReason != "" {
			result.Status = StatusMissing
			result.Message = file.MissingReason
			results = append(results, result)
			continue
		}
		for _, block := range file.Blocks {
			err, checked := objectStatus[block.ObjectPath]
			if !checked {
				info, statErr := e.Repository.StatObject(ctx, task.Name, block.ObjectPath)
				if statErr == nil && info.Size != block.ObjectSize {
					statErr = fmt.Errorf("对象大小应为%d，实际为%d", block.ObjectSize, info.Size)
				}
				err = statErr
				objectStatus[block.ObjectPath] = err
			}
			if err != nil {
				result.Status = StatusMissing
				result.Message = fmt.Sprintf("缺少或无法读取块 %s: %v", block.ObjectPath, err)
				break
			}
		}
		results = append(results, result)
	}
	return results
}

func (e *Engine) Restore(ctx context.Context, task model.Task, snapshot model.Snapshot, selected []string, outputRoot string) (Report, error) {
	report := Report{StartedAt: time.Now().UTC(), SnapshotID: snapshot.ID, Results: e.Preflight(ctx, task, snapshot, selected)}
	defer func() { report.FinishedAt = time.Now().UTC() }()
	if err := ensureOutputRoot(outputRoot); err != nil {
		return report, err
	}
	salt, err := cryptox.DecodeSalt(task.Salt)
	if err != nil {
		return report, err
	}
	key, err := cryptox.DeriveKey(task.Password, salt, cryptox.DefaultKDFParams())
	if err != nil {
		return report, err
	}

	entries := map[string]model.FileEntry{}
	states := map[string]*outputState{}
	casePaths := map[string]string{}
	for index := range report.Results {
		result := &report.Results[index]
		if result.Status != StatusReady {
			continue
		}
		entry, found := findEntry(snapshot.Files, result.RootAlias, result.RelativePath)
		if !found {
			result.Status = StatusFailed
			result.Message = "索引条目不存在"
			continue
		}
		if runtime.GOOS == "windows" && !validWindowsPath(entry.RootAlias, entry.RelativePath) {
			result.Status = StatusInvalid
			result.Message = "Windows不支持该文件名"
			continue
		}
		target, conflict, targetErr := chooseTarget(outputRoot, entry, casePaths)
		if targetErr != nil {
			result.Status = StatusFailed
			result.Message = targetErr.Error()
			continue
		}
		if conflict == existingSame {
			result.Status = StatusSkipped
			result.OutputPath = target
			_ = fsmeta.SetTimes(target, entry.Times.Created, entry.Times.Modified)
			continue
		}
		if conflict == existingDifferent {
			result.Status = StatusConflict
		}
		result.OutputPath = target
		temp := target + ".wcb-restore.partial"
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			result.Status = StatusFailed
			result.Message = err.Error()
			continue
		}
		if err := os.Remove(temp); err != nil && !os.IsNotExist(err) {
			result.Status = StatusFailed
			result.Message = err.Error()
			continue
		}
		entries[entry.ID] = entry
		states[entry.ID] = &outputState{tempPath: temp, targetPath: target}
	}

	objectFiles := map[string][]string{}
	for fileID, entry := range entries {
		for _, block := range entry.Blocks {
			objectFiles[block.ObjectPath] = append(objectFiles[block.ObjectPath], fileKey(entry.RootAlias, entry.RelativePath))
		}
		_ = fileID
	}
	objectPaths := make([]string, 0, len(objectFiles))
	for objectPath := range objectFiles {
		objectPaths = append(objectPaths, objectPath)
	}
	sort.Strings(objectPaths)
	for _, objectPath := range objectPaths {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		reader, openErr := e.Repository.OpenObject(ctx, task.Name, objectPath)
		if openErr != nil {
			markFailed(report.Results, objectFiles[objectPath], openErr.Error())
			continue
		}
		objectReader, decryptErr := cryptox.OpenObjectWithKey(reader, key)
		if decryptErr != nil {
			_ = reader.Close()
			markFailed(report.Results, objectFiles[objectPath], decryptErr.Error())
			continue
		}
		var metadata model.ObjectPayloadMetadata
		if err := json.Unmarshal(objectReader.Metadata, &metadata); err != nil {
			_ = reader.Close()
			markFailed(report.Results, objectFiles[objectPath], err.Error())
			continue
		}
		writeErr := extractSelected(objectReader.Payload, metadata, states)
		_ = reader.Close()
		if writeErr != nil {
			markFailed(report.Results, objectFiles[objectPath], writeErr.Error())
		}
	}

	for fileID, state := range states {
		entry := entries[fileID]
		result := findResult(report.Results, entry.RootAlias, entry.RelativePath)
		if result == nil || result.Status == StatusFailed {
			_ = os.Remove(state.tempPath)
			continue
		}
		if state.written != entry.Size {
			result.Status = StatusFailed
			result.Message = fmt.Sprintf("恢复字节数不完整：需要%d，实际%d", entry.Size, state.written)
			_ = os.Remove(state.tempPath)
			continue
		}
		hash, hashErr := hashFile(state.tempPath)
		if hashErr != nil || hash != entry.Hash {
			result.Status = StatusFailed
			result.Message = "恢复后SHA-256校验失败"
			_ = os.Remove(state.tempPath)
			continue
		}
		if err := os.Rename(state.tempPath, state.targetPath); err != nil {
			result.Status = StatusFailed
			result.Message = err.Error()
			continue
		}
		_ = fsmeta.SetTimes(state.targetPath, entry.Times.Created, entry.Times.Modified)
		if result.Status != StatusConflict {
			result.Status = StatusRestored
		}
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

type outputState struct {
	tempPath   string
	targetPath string
	written    int64
}

func extractSelected(payload io.Reader, metadata model.ObjectPayloadMetadata, states map[string]*outputState) error {
	records := append([]model.PayloadFileRecord(nil), metadata.Files...)
	sort.Slice(records, func(i, j int) bool { return records[i].Offset < records[j].Offset })
	var position int64
	for _, record := range records {
		if record.Offset < position {
			return errors.New("对象元数据偏移无效")
		}
		if _, err := io.CopyN(io.Discard, payload, record.Offset-position); err != nil {
			return err
		}
		state := states[record.FileID]
		if state == nil {
			if _, err := io.CopyN(io.Discard, payload, record.Length); err != nil {
				return err
			}
		} else {
			file, err := os.OpenFile(state.tempPath, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			section := io.NewOffsetWriter(file, record.FileOffset)
			written, copyErr := io.CopyN(section, payload, record.Length)
			closeErr := file.Close()
			state.written += written
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		position = record.Offset + record.Length
	}
	_, err := io.Copy(io.Discard, payload)
	return err
}

type conflictKind int

const (
	noConflict conflictKind = iota
	existingSame
	existingDifferent
)

func chooseTarget(root string, entry model.FileEntry, casePaths map[string]string) (string, conflictKind, error) {
	target, err := safeJoin(root, entry.RootAlias, entry.RelativePath)
	if err != nil {
		return "", noConflict, err
	}
	caseKey := strings.ToLower(target)
	if previous, exists := casePaths[caseKey]; exists && previous != target {
		return "", noConflict, errors.New("目标文件系统存在大小写路径冲突")
	}
	casePaths[caseKey] = target
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return target, noConflict, nil
	}
	if err != nil {
		return "", noConflict, err
	}
	if info.IsDir() {
		return conflictTarget(root, entry), existingDifferent, nil
	}
	hash, err := hashFile(target)
	if err == nil && hash == entry.Hash {
		return target, existingSame, nil
	}
	return conflictTarget(root, entry), existingDifferent, nil
}

func conflictTarget(root string, entry model.FileEntry) string {
	base, _ := safeJoin(root, "冲突文件", entry.RootAlias, entry.RelativePath)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, index, extension)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func safeJoin(root string, parts ...string) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	all := []string{absoluteRoot}
	for _, part := range parts {
		all = append(all, filepath.FromSlash(part))
	}
	joined := filepath.Clean(filepath.Join(all...))
	relative, err := filepath.Rel(absoluteRoot, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("恢复路径越过目标目录")
	}
	return joined, nil
}

func ensureOutputRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("恢复目标目录不能为空")
	}
	return os.MkdirAll(root, 0o755)
}

func validWindowsPath(alias, relative string) bool {
	for _, component := range append([]string{alias}, strings.Split(filepath.ToSlash(relative), "/")...) {
		if component == "" || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") || strings.ContainsAny(component, `<>:"\\|?*`) {
			return false
		}
		upper := strings.ToUpper(strings.TrimSuffix(component, filepath.Ext(component)))
		if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" || strings.HasPrefix(upper, "COM") && len(upper) == 4 || strings.HasPrefix(upper, "LPT") && len(upper) == 4 {
			return false
		}
	}
	return true
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func selectionMap(selected []string) map[string]struct{} {
	selection := make(map[string]struct{}, len(selected))
	for _, item := range selected {
		selection[filepath.ToSlash(strings.Trim(item, "/"))] = struct{}{}
	}
	return selection
}

func selectedFile(selection map[string]struct{}, key string) bool {
	if len(selection) == 0 {
		return true
	}
	if _, exists := selection[key]; exists {
		return true
	}
	for selected := range selection {
		if strings.HasPrefix(key, selected+"/") {
			return true
		}
	}
	return false
}

func fileKey(alias, relative string) string {
	return filepath.ToSlash(filepath.Join(alias, filepath.FromSlash(relative)))
}

func findEntry(entries []model.FileEntry, alias, relative string) (model.FileEntry, bool) {
	for _, entry := range entries {
		if entry.RootAlias == alias && entry.RelativePath == relative {
			return entry, true
		}
	}
	return model.FileEntry{}, false
}

func findResult(results []FileResult, alias, relative string) *FileResult {
	for index := range results {
		if results[index].RootAlias == alias && results[index].RelativePath == relative {
			return &results[index]
		}
	}
	return nil
}

func markFailed(results []FileResult, keys []string, message string) {
	selected := selectionMap(keys)
	for index := range results {
		key := fileKey(results[index].RootAlias, results[index].RelativePath)
		if _, exists := selected[key]; exists {
			results[index].Status = StatusFailed
			results[index].Message = message
		}
	}
}

func WriteReport(directory string, report Report) (string, string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", "", err
	}
	base := "恢复报告-" + time.Now().Format("20060102-150405")
	jsonPath := filepath.Join(directory, base+".json")
	htmlPath := filepath.Join(directory, base+".html")
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(jsonPath, encoded, 0o600); err != nil {
		return "", "", err
	}
	html := "<!doctype html><meta charset=\"utf-8\"><title>恢复报告</title><h1>WebDAV冷备份恢复报告</h1><pre>" + htmlEscape(string(encoded)) + "</pre>"
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		return "", "", err
	}
	return jsonPath, htmlPath, nil
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(value)
}
