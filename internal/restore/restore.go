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
	Verified     bool       `json:"verified"`
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

type Progress struct {
	Status               string    `json:"status"`
	Phase                string    `json:"phase"`
	Percent              float64   `json:"percent"`
	Message              string    `json:"message"`
	CurrentObject        string    `json:"currentObject,omitempty"`
	CurrentFile          string    `json:"currentFile,omitempty"`
	FilesCompleted       int       `json:"filesCompleted"`
	FilesTotal           int       `json:"filesTotal"`
	ObjectsChecked       int       `json:"objectsChecked"`
	ObjectsCheckTotal    int       `json:"objectsCheckTotal"`
	ObjectsCompleted     int       `json:"objectsCompleted"`
	ObjectsTotal         int       `json:"objectsTotal"`
	BytesCompleted       int64     `json:"bytesCompleted"`
	BytesTotal           int64     `json:"bytesTotal"`
	VerifyBytesCompleted int64     `json:"verifyBytesCompleted"`
	VerifyBytesTotal     int64     `json:"verifyBytesTotal"`
	RestoredFiles        int       `json:"restoredFiles"`
	SkippedFiles         int       `json:"skippedFiles"`
	FailedFiles          int       `json:"failedFiles"`
	VerifiedFiles        int       `json:"verifiedFiles"`
	Error                string    `json:"error,omitempty"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type Engine struct {
	Repository *repository.Repository
	Progress   func(Progress)
}

type progressEmitter struct {
	callback func(Progress)
	last     time.Time
}

func (e *progressEmitter) emit(progress *Progress, force bool) {
	if e.callback == nil {
		return
	}
	now := time.Now().UTC()
	if !force && !e.last.IsZero() && now.Sub(e.last) < 200*time.Millisecond {
		return
	}
	progress.UpdatedAt = now
	e.last = now
	e.callback(*progress)
}

func restoreTotals(snapshot model.Snapshot, selected []string) (int, map[string]int64, int64) {
	selection := selectionMap(selected)
	objects := map[string]int64{}
	files := 0
	var verifyBytes int64
	for _, file := range snapshot.Files {
		key := fileKey(file.RootAlias, file.RelativePath)
		if !selectedFile(selection, key) {
			continue
		}
		files++
		if file.MissingReason != "" {
			continue
		}
		verifyBytes += file.Size
		for _, block := range file.Blocks {
			objects[block.ObjectPath] = block.ObjectSize
		}
	}
	return files, objects, verifyBytes
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
	return e.preflight(ctx, task, snapshot, selected, nil)
}

func (e *Engine) preflight(ctx context.Context, task model.Task, snapshot model.Snapshot, selected []string, onObject func(string)) []FileResult {
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
				if onObject != nil {
					onObject(block.ObjectPath)
				}
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

func (e *Engine) Restore(ctx context.Context, task model.Task, snapshot model.Snapshot, selected []string, outputRoot string) (report Report, finalErr error) {
	startedAt := time.Now().UTC()
	fileTotal, objectSizes, verifyBytesTotal := restoreTotals(snapshot, selected)
	var bytesTotal int64
	for _, size := range objectSizes {
		bytesTotal += size
	}
	progress := Progress{
		Status: "running", Phase: "preflight", Percent: 1, Message: "正在核对恢复索引和数据对象",
		FilesTotal: fileTotal, ObjectsCheckTotal: len(objectSizes), ObjectsTotal: len(objectSizes), BytesTotal: bytesTotal,
		VerifyBytesTotal: verifyBytesTotal, UpdatedAt: startedAt,
	}
	emitter := progressEmitter{callback: e.Progress}
	emitter.emit(&progress, true)
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if finalErr != nil {
			progress.Status = "failed"
			progress.Phase = "failed"
			progress.Message = "恢复失败"
			progress.Error = finalErr.Error()
			updateProgressCounts(&progress, report.Results)
			emitter.emit(&progress, true)
		}
	}()
	report = Report{StartedAt: startedAt, SnapshotID: snapshot.ID}
	report.Results = e.preflight(ctx, task, snapshot, selected, func(objectPath string) {
		progress.CurrentObject = objectPath
		progress.ObjectsChecked++
		if progress.ObjectsCheckTotal > 0 {
			progress.Percent = 1 + 9*float64(progress.ObjectsChecked)/float64(progress.ObjectsCheckTotal)
		}
		emitter.emit(&progress, false)
	})
	progress.CurrentObject = ""
	progress.FilesTotal = len(report.Results)
	updateProgressCounts(&progress, report.Results)
	emitter.emit(&progress, true)
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

	progress.Phase = "preparing"
	progress.Percent = 10
	progress.Message = "正在准备恢复路径并核对已有文件"
	emitter.emit(&progress, true)
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
			updateProgressCounts(&progress, report.Results)
			continue
		}
		progress.CurrentFile = fileKey(entry.RootAlias, entry.RelativePath)
		var existingHashBytes int64
		target, conflict, targetErr := chooseTargetWithProgress(outputRoot, entry, casePaths, func(count int64) {
			existingHashBytes += count
			progress.VerifyBytesCompleted += count
			progress.Message = "正在哈希核对已有文件"
			emitter.emit(&progress, false)
		})
		if targetErr != nil {
			result.Status = StatusFailed
			result.Message = targetErr.Error()
			updateProgressCounts(&progress, report.Results)
			continue
		}
		if conflict == existingSame {
			result.Status = StatusSkipped
			result.OutputPath = target
			result.Verified = true
			_ = fsmeta.SetTimes(target, entry.Times.Created, entry.Times.Modified)
			updateProgressCounts(&progress, report.Results)
			emitter.emit(&progress, true)
			continue
		}
		if conflict == existingDifferent {
			result.Status = StatusConflict
			if existingHashBytes > 0 {
				progress.VerifyBytesTotal += entry.Size
			}
		}
		result.OutputPath = target
		temp := target + ".wcb-restore.partial"
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			result.Status = StatusFailed
			result.Message = err.Error()
			updateProgressCounts(&progress, report.Results)
			continue
		}
		if err := os.Remove(temp); err != nil && !os.IsNotExist(err) {
			result.Status = StatusFailed
			result.Message = err.Error()
			updateProgressCounts(&progress, report.Results)
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
	progress.Phase = "restoring"
	progress.Percent = 15
	progress.Message = "正在解密并写入恢复文件"
	progress.CurrentFile = ""
	progress.ObjectsTotal = len(objectPaths)
	progress.BytesTotal = 0
	for _, objectPath := range objectPaths {
		progress.BytesTotal += objectSizes[objectPath]
	}
	emitter.emit(&progress, true)
	var completedObjectBytes int64
	completeObject := func(objectPath string) {
		completedObjectBytes += objectSizes[objectPath]
		progress.BytesCompleted = completedObjectBytes
		progress.ObjectsCompleted++
		if progress.BytesTotal > 0 {
			progress.Percent = 15 + 70*float64(progress.BytesCompleted)/float64(progress.BytesTotal)
		}
		updateProgressCounts(&progress, report.Results)
		emitter.emit(&progress, true)
	}
	for _, objectPath := range objectPaths {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		progress.CurrentObject = objectPath
		progress.Message = "正在读取并解密数据对象"
		emitter.emit(&progress, true)
		reader, openErr := e.Repository.OpenObject(ctx, task.Name, objectPath)
		if openErr != nil {
			markFailed(report.Results, objectFiles[objectPath], openErr.Error())
			completeObject(objectPath)
			continue
		}
		objectReader, decryptErr := cryptox.OpenObjectWithKey(reader, key)
		if decryptErr != nil {
			_ = reader.Close()
			markFailed(report.Results, objectFiles[objectPath], decryptErr.Error())
			completeObject(objectPath)
			continue
		}
		var metadata model.ObjectPayloadMetadata
		if err := json.Unmarshal(objectReader.Metadata, &metadata); err != nil {
			_ = reader.Close()
			markFailed(report.Results, objectFiles[objectPath], err.Error())
			completeObject(objectPath)
			continue
		}
		objectSize := objectSizes[objectPath]
		var currentObjectBytes int64
		payload := &countingReader{reader: objectReader.Payload, onRead: func(count int64) {
			currentObjectBytes += count
			progress.BytesCompleted = completedObjectBytes + min(currentObjectBytes, objectSize)
			if progress.BytesTotal > 0 {
				progress.Percent = 15 + 70*float64(progress.BytesCompleted)/float64(progress.BytesTotal)
			}
			emitter.emit(&progress, false)
		}}
		writeErr := extractSelected(payload, metadata, states, func(record model.PayloadFileRecord) {
			progress.CurrentFile = fileKey(record.RootAlias, record.RelativePath)
			emitter.emit(&progress, false)
		})
		_ = reader.Close()
		if writeErr != nil {
			markFailed(report.Results, objectFiles[objectPath], writeErr.Error())
		}
		completeObject(objectPath)
	}

	progress.Phase = "verifying"
	progress.Percent = 85
	progress.Message = "正在对恢复结果执行SHA-256核验"
	progress.CurrentObject = ""
	emitter.emit(&progress, true)
	fileIDs := make([]string, 0, len(states))
	for fileID := range states {
		fileIDs = append(fileIDs, fileID)
	}
	sort.Slice(fileIDs, func(i, j int) bool {
		left, right := entries[fileIDs[i]], entries[fileIDs[j]]
		return fileKey(left.RootAlias, left.RelativePath) < fileKey(right.RootAlias, right.RelativePath)
	})
	for _, fileID := range fileIDs {
		state := states[fileID]
		entry := entries[fileID]
		result := findResult(report.Results, entry.RootAlias, entry.RelativePath)
		progress.CurrentFile = fileKey(entry.RootAlias, entry.RelativePath)
		if result == nil || result.Status == StatusFailed {
			_ = os.Remove(state.tempPath)
			updateProgressCounts(&progress, report.Results)
			continue
		}
		if state.written != entry.Size {
			result.Status = StatusFailed
			result.Message = fmt.Sprintf("恢复字节数不完整：需要%d，实际%d", entry.Size, state.written)
			_ = os.Remove(state.tempPath)
			updateProgressCounts(&progress, report.Results)
			emitter.emit(&progress, true)
			continue
		}
		hash, hashErr := hashFileWithProgress(state.tempPath, func(count int64) {
			progress.VerifyBytesCompleted += count
			if progress.VerifyBytesTotal > 0 {
				progress.Percent = 85 + 14*float64(progress.VerifyBytesCompleted)/float64(progress.VerifyBytesTotal)
				progress.Percent = min(progress.Percent, 99)
			}
			emitter.emit(&progress, false)
		})
		if hashErr != nil || hash != entry.Hash {
			result.Status = StatusFailed
			result.Message = "恢复后SHA-256校验失败"
			_ = os.Remove(state.tempPath)
			updateProgressCounts(&progress, report.Results)
			emitter.emit(&progress, true)
			continue
		}
		if err := os.Rename(state.tempPath, state.targetPath); err != nil {
			result.Status = StatusFailed
			result.Message = err.Error()
			updateProgressCounts(&progress, report.Results)
			emitter.emit(&progress, true)
			continue
		}
		_ = fsmeta.SetTimes(state.targetPath, entry.Times.Created, entry.Times.Modified)
		result.Verified = true
		if result.Status != StatusConflict {
			result.Status = StatusRestored
		}
		updateProgressCounts(&progress, report.Results)
		emitter.emit(&progress, true)
	}
	progress.Status = "completed"
	progress.Phase = "completed"
	progress.Percent = 100
	progress.Message = "恢复和SHA-256核验完成"
	progress.CurrentObject = ""
	progress.CurrentFile = ""
	updateProgressCounts(&progress, report.Results)
	emitter.emit(&progress, true)
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

type outputState struct {
	tempPath   string
	targetPath string
	written    int64
}

type countingReader struct {
	reader io.Reader
	onRead func(int64)
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if count > 0 && r.onRead != nil {
		r.onRead(int64(count))
	}
	return count, err
}

func extractSelected(payload io.Reader, metadata model.ObjectPayloadMetadata, states map[string]*outputState, onFile func(model.PayloadFileRecord)) error {
	records := append([]model.PayloadFileRecord(nil), metadata.Files...)
	sort.Slice(records, func(i, j int) bool { return records[i].Offset < records[j].Offset })
	var position int64
	for _, record := range records {
		if onFile != nil {
			onFile(record)
		}
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
	return chooseTargetWithProgress(root, entry, casePaths, nil)
}

func chooseTargetWithProgress(root string, entry model.FileEntry, casePaths map[string]string, onHash func(int64)) (string, conflictKind, error) {
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
	hash, err := hashFileWithProgress(target, onHash)
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
	return hashFileWithProgress(path, nil)
}

func hashFileWithProgress(path string, onRead func(int64)) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	reader := io.Reader(file)
	if onRead != nil {
		reader = &countingReader{reader: file, onRead: onRead}
	}
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func updateProgressCounts(progress *Progress, results []FileResult) {
	progress.FilesCompleted = 0
	progress.RestoredFiles = 0
	progress.SkippedFiles = 0
	progress.FailedFiles = 0
	progress.VerifiedFiles = 0
	for _, result := range results {
		if result.Verified {
			progress.VerifiedFiles++
		}
		switch result.Status {
		case StatusRestored, StatusConflict:
			progress.FilesCompleted++
			progress.RestoredFiles++
		case StatusSkipped:
			progress.FilesCompleted++
			progress.SkippedFiles++
		case StatusMissing, StatusInvalid, StatusFailed:
			progress.FilesCompleted++
			progress.FailedFiles++
		}
	}
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
