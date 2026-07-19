package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/object"
	"github.com/ccawmiku/webdav-cold-backup/internal/repository"
	"github.com/ccawmiku/webdav-cold-backup/internal/scanner"
	"github.com/ccawmiku/webdav-cold-backup/internal/state"
	"github.com/google/uuid"
)

var errSourceChanged = errors.New("source changed while creating an object")

type Engine struct {
	State       *state.Store
	CacheRoot   string
	Control     *Control
	RetryDelays []time.Duration
	Now         func() time.Time
	Progress    func(model.TaskProgress)
}

func NewEngine(store *state.Store, cacheRoot string, control *Control) *Engine {
	if control == nil {
		control = NewControl()
	}
	return &Engine{
		State: store, CacheRoot: cacheRoot, Control: control,
		RetryDelays: []time.Duration{0, time.Minute, 5 * time.Minute, 25 * time.Minute},
		Now:         time.Now,
	}
}

type preparedFile struct {
	scanned  scanner.File
	source   object.SourceFile
	previous *model.FileEntry
}

type buildJob struct {
	spec    object.BuildSpec
	fileIDs []string
	label   string
	bytes   int64
}

type jobResult struct {
	built   object.BuiltObject
	fileIDs []string
	label   string
	bytes   int64
	err     error
}

func (e *Engine) Run(ctx context.Context, task model.Task, repo *repository.Repository, settings model.GlobalSettings) (model.RunRecord, error) {
	now := e.Now().UTC()
	run := model.RunRecord{
		ID: strings.ReplaceAll(uuid.NewString(), "-", ""), TaskID: task.ID,
		Status: model.RunRunning, StartedAt: now, Details: []string{},
	}
	progress := model.TaskProgress{TaskID: task.ID, Phase: "scanning", Percent: 1, Message: "正在扫描源目录"}
	reportProgress := func(update model.TaskProgress) {
		update.TaskID = task.ID
		update.UpdatedAt = e.Now().UTC()
		progress = update
		if e.Progress != nil {
			e.Progress(update)
		}
	}
	reportProgress(progress)
	_ = e.State.SaveRun(ctx, run)
	finish := func(status model.RunStatus, message string, runErr error) (model.RunRecord, error) {
		finished := e.Now().UTC()
		run.Status = status
		run.Message = message
		run.FinishedAt = &finished
		_ = e.State.SaveRun(context.Background(), run)
		progress.Message = message
		switch status {
		case model.RunComplete:
			progress.Phase = "completed"
			progress.Percent = 100
		case model.RunIncomplete:
			progress.Phase = "incomplete"
			progress.Percent = 100
		case model.RunFailed:
			progress.Phase = "failed"
		}
		reportProgress(progress)
		return run, runErr
	}

	scan, err := scanner.Scan(ctx, task.Sources, model.DefaultStablePeriod, now)
	if err != nil {
		return finish(model.RunFailed, err.Error(), err)
	}
	run.FilesScanned = len(scan.Files)
	progress = model.TaskProgress{
		TaskID: task.ID, Phase: "hashing", Percent: 10, Message: "正在比较文件并计算新增内容哈希",
		FilesTotal: len(scan.Files),
	}
	reportProgress(progress)
	if scan.IgnoredSymlinks > 0 {
		run.Details = append(run.Details, fmt.Sprintf("已忽略%d个符号链接", scan.IgnoredSymlinks))
	}
	if scan.IgnoredSystem > 0 {
		run.Details = append(run.Details, fmt.Sprintf("已忽略%d个回收站或系统目录", scan.IgnoredSystem))
	}
	previous, hasPrevious, err := e.previousState(ctx, task)
	if err != nil {
		return finish(model.RunFailed, err.Error(), err)
	}

	baseEntries := previous.Files
	byPath := make(map[string]model.FileEntry, len(baseEntries))
	byHash := make(map[string]model.FileEntry, len(baseEntries))
	for _, entry := range baseEntries {
		byPath[fileKey(entry.RootAlias, entry.RelativePath)] = entry
		if entry.Hash != "" && entry.MissingReason == "" {
			if _, exists := byHash[entry.Hash]; !exists {
				byHash[entry.Hash] = entry
			}
		}
	}

	entries := []model.FileEntry{}
	if task.Mode == model.TaskModeArchive {
		entries = append(entries, cloneEntries(baseEntries)...)
	}
	prepared := []preparedFile{}
	seenPaths := make(map[string]struct{}, len(scan.Files))
	currentPaths := make(map[string]struct{}, len(scan.Files)+len(scan.UnstableFiles))
	for _, scanned := range scan.Files {
		currentPaths[fileKey(scanned.RootAlias, scanned.RelativePath)] = struct{}{}
	}
	for _, unstable := range scan.UnstableFiles {
		currentPaths[fileKey(unstable.RootAlias, unstable.RelativePath)] = struct{}{}
	}
	pendingArchiveHashes := make(map[string]struct{})
	changeCount := 0

	for index, scanned := range scan.Files {
		progress.CurrentFile = displayPath(scanned.RootAlias, scanned.RelativePath)
		progress.FilesProcessed = index
		if progress.FilesTotal > 0 {
			progress.Percent = 10 + 30*float64(index)/float64(progress.FilesTotal)
		}
		reportProgress(progress)
		key := fileKey(scanned.RootAlias, scanned.RelativePath)
		seenPaths[key] = struct{}{}
		old, pathExists := byPath[key]
		if pathExists && sameMetadata(old, scanned) {
			if task.Mode == model.TaskModeSnapshot {
				entries = append(entries, old)
			}
			continue
		}
		hash, hashErr := object.HashFile(ctx, scanned.AbsolutePath)
		if hashErr != nil {
			if task.Mode == model.TaskModeSnapshot && pathExists {
				entries = append(entries, old)
			}
			continue
		}
		if changedSinceScan(scanned) {
			if task.Mode == model.TaskModeSnapshot && pathExists {
				entries = append(entries, old)
			}
			continue
		}
		if reusable, exists := byHash[hash]; exists {
			if task.Mode == model.TaskModeArchive {
				continue
			}
			reusableKey := fileKey(reusable.RootAlias, reusable.RelativePath)
			if _, originalStillPresent := currentPaths[reusableKey]; !originalStillPresent || reusableKey == key {
				moved := reusable
				oldDisplayPath := displayPath(moved.RootAlias, moved.RelativePath)
				moved.RootAlias = scanned.RootAlias
				moved.RelativePath = scanned.RelativePath
				moved.Size = scanned.Size
				moved.Times = model.FileTimes{Modified: scanned.Modified, Created: scanned.Created}
				if oldDisplayPath != displayPath(moved.RootAlias, moved.RelativePath) {
					moved.HistoricalPath = appendUnique(moved.HistoricalPath, oldDisplayPath)
				}
				entries = append(entries, moved)
				changeCount++
				continue
			}
		}
		if task.Mode == model.TaskModeArchive {
			if _, alreadyPending := pendingArchiveHashes[hash]; alreadyPending {
				continue
			}
			pendingArchiveHashes[hash] = struct{}{}
		}
		relativePath := scanned.RelativePath
		if task.Mode == model.TaskModeArchive && pathExists {
			relativePath = archiveConflictPath(relativePath, hash)
		}
		source := object.NewSourceFile(scanned.RootAlias, relativePath, scanned.AbsolutePath, scanned.Size, hash, scanned.Modified, scanned.Created)
		item := preparedFile{scanned: scanned, source: source}
		if pathExists {
			oldCopy := old
			item.previous = &oldCopy
		}
		prepared = append(prepared, item)
		changeCount++
	}
	progress.FilesProcessed = len(scan.Files)
	progress.CurrentFile = ""
	progress.Percent = 40
	reportProgress(progress)

	if task.Mode == model.TaskModeSnapshot {
		for _, unstable := range scan.UnstableFiles {
			key := fileKey(unstable.RootAlias, unstable.RelativePath)
			seenPaths[key] = struct{}{}
			if old, exists := byPath[key]; exists && !containsEntry(entries, key) {
				entries = append(entries, old)
			}
		}
		for key := range byPath {
			if _, exists := seenPaths[key]; !exists {
				changeCount++
			}
		}
		if !hasPrevious {
			changeCount += len(entries)
		}
	}

	if task.Mode == model.TaskModeSnapshot && hasPrevious && changeCount == 0 && len(prepared) == 0 && sameSources(previous.Sources, task.Sources) {
		task.LastRunAt = &now
		task.Status = model.TaskIdle
		task.UpdatedAt = now
		_ = e.State.SaveTask(ctx, task)
		return finish(model.RunComplete, "扫描完成，没有变化，未创建新快照", nil)
	}

	keyBytes, err := deriveTaskKey(task)
	if err != nil {
		return finish(model.RunFailed, err.Error(), err)
	}
	jobs, err := e.planJobs(task, keyBytes, prepared)
	if err != nil {
		return finish(model.RunFailed, err.Error(), err)
	}
	var uploadBytesTotal int64
	for _, job := range jobs {
		uploadBytesTotal += job.bytes
	}
	progress.Phase = "uploading"
	progress.Message = "正在加密并上传数据对象"
	progress.Percent = 45
	progress.ObjectsTotal = len(jobs)
	progress.BytesTotal = uploadBytesTotal
	if len(jobs) > 0 {
		progress.CurrentFile = jobs[0].label
	}
	reportProgress(progress)
	results := e.executeJobs(ctx, task, repo, settings, jobs)
	blocksByFile := make(map[string][]model.BlockRef)
	objects := []model.ObjectRecord{}
	failedFiles := make(map[string]string)
	for result := range results {
		progress.ObjectsCompleted++
		progress.BytesCompleted += result.bytes
		progress.CurrentFile = result.label
		if progress.ObjectsTotal > 0 {
			progress.Percent = 45 + 45*float64(progress.ObjectsCompleted)/float64(progress.ObjectsTotal)
		}
		reportProgress(progress)
		if result.err != nil {
			for _, fileID := range result.fileIDs {
				failedFiles[fileID] = result.err.Error()
			}
			run.Details = append(run.Details, result.err.Error())
			continue
		}
		run.BytesUploaded += result.built.Record.Size
		objects = append(objects, result.built.Record)
		for _, payloadFile := range result.built.Metadata.Files {
			blocksByFile[payloadFile.FileID] = append(blocksByFile[payloadFile.FileID], model.BlockRef{
				ObjectPath: result.built.Record.Path, ObjectID: result.built.Record.ID,
				GroupID: result.built.Record.GroupID, Part: result.built.Record.Part,
				TotalParts: result.built.Record.TotalParts, Offset: payloadFile.Offset,
				Length: payloadFile.Length, FileOffset: payloadFile.FileOffset,
				ObjectSize: result.built.Record.Size, ObjectHash: result.built.Record.Hash,
			})
		}
	}
	progress.Phase = "finalizing"
	progress.Percent = 92
	progress.Message = "正在发布索引并执行版本保留检查"
	progress.CurrentFile = ""
	reportProgress(progress)

	missing := []string{}
	for _, item := range prepared {
		failure := failedFiles[item.source.ID]
		blocks := blocksByFile[item.source.ID]
		if failure == "" && !blocksCoverFile(blocks, item.source.Size) {
			failure = "一个或多个分块上传失败"
		}
		entry := model.FileEntry{
			ID: item.source.ID, RootAlias: item.source.RootAlias,
			RelativePath: item.source.RelativePath, Size: item.source.Size,
			Hash: item.source.Hash, Times: item.source.Times,
		}
		if failure != "" {
			entry.MissingReason = failure
			missing = append(missing, displayPath(entry.RootAlias, entry.RelativePath))
		} else {
			sort.Slice(blocks, func(i, j int) bool { return blocks[i].FileOffset < blocks[j].FileOffset })
			entry.Blocks = blocks
			run.FilesAdded++
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return fileKey(entries[i].RootAlias, entries[i].RelativePath) < fileKey(entries[j].RootAlias, entries[j].RelativePath)
	})
	allObjects := recordsFromEntries(entries)
	snapshot := model.Snapshot{
		ID: snapshotID(now), TaskID: task.ID, CreatedAt: now,
		Complete: len(missing) == 0, Sources: append([]model.SourceRoot(nil), task.Sources...),
		Files: entries, Objects: allObjects, MissingFiles: missing, ChangeCount: changeCount,
	}

	if task.Mode == model.TaskModeArchive {
		snapshot.ID = "archive"
		if hasPrevious {
			snapshot.CreatedAt = previous.CreatedAt
		}
		if err := e.State.SaveSnapshot(ctx, snapshot); err != nil {
			return finish(model.RunFailed, err.Error(), err)
		}
		catalog := catalogForTask(task)
		catalog.Archive = &snapshot
		if err := repo.SaveCatalog(ctx, task, catalog); err != nil {
			return finish(model.RunFailed, err.Error(), err)
		}
	} else {
		if err := repo.SaveSnapshot(ctx, task, snapshot); err != nil {
			return finish(model.RunFailed, err.Error(), err)
		}
		if err := e.State.SaveSnapshot(ctx, snapshot); err != nil {
			return finish(model.RunFailed, err.Error(), err)
		}
		if err := e.applyRetention(ctx, task, repo); err != nil {
			run.Details = append(run.Details, "版本清理失败: "+err.Error())
		}
		catalog, err := e.catalogFromState(ctx, task)
		if err != nil {
			return finish(model.RunFailed, err.Error(), err)
		}
		if err := repo.SaveCatalog(ctx, task, catalog); err != nil {
			return finish(model.RunFailed, err.Error(), err)
		}
	}

	task.LastRunAt = &now
	task.Status = model.TaskIdle
	task.UpdatedAt = now
	_ = e.State.SaveTask(ctx, task)
	if len(missing) > 0 {
		return finish(model.RunIncomplete, fmt.Sprintf("备份完成，但有%d个文件不完整", len(missing)), nil)
	}
	return finish(model.RunComplete, "备份完成", nil)
}

func (e *Engine) previousState(ctx context.Context, task model.Task) (model.Snapshot, bool, error) {
	items, err := e.State.Snapshots(ctx, task.ID)
	if err != nil {
		return model.Snapshot{}, false, err
	}
	if task.Mode == model.TaskModeArchive {
		for _, item := range items {
			if item.ID == "archive" {
				return item, true, nil
			}
		}
		return model.Snapshot{ID: "archive", TaskID: task.ID, CreatedAt: e.Now().UTC(), Complete: true, Sources: task.Sources}, false, nil
	}
	for _, item := range items {
		if item.ID != "archive" {
			return item, true, nil
		}
	}
	return model.Snapshot{}, false, nil
}

func (e *Engine) planJobs(task model.Task, key []byte, files []preparedFile) ([]buildJob, error) {
	maxPayload, err := object.MaxPayloadSize(task.BlockSize)
	if err != nil {
		return nil, err
	}
	cacheDir := filepath.Join(e.CacheRoot, task.ID)
	largeThreshold := maxPayload * 80 / 100
	jobs := []buildJob{}
	pack := []object.Slice{}
	var packSize int64
	flushPack := func() {
		if len(pack) == 0 {
			return
		}
		groupID := object.NewGroupID()
		ids := make([]string, 0, len(pack))
		for _, slice := range pack {
			ids = append(ids, slice.File.ID)
		}
		jobs = append(jobs, buildJob{
			spec:    object.BuildSpec{TaskID: task.ID, EncodedSalt: task.Salt, Key: key, MaxObjectSize: task.BlockSize, GroupID: groupID, Part: 1, TotalParts: 1, Kind: "pack", Slices: pack, CacheDir: cacheDir},
			fileIDs: ids,
		})
		pack = nil
		packSize = 0
	}
	for _, item := range files {
		if item.source.Size > largeThreshold {
			flushPack()
			parts := int((item.source.Size + maxPayload - 1) / maxPayload)
			if parts == 0 {
				parts = 1
			}
			groupID := object.NewGroupID()
			for part := 0; part < parts; part++ {
				offset := int64(part) * maxPayload
				length := min(maxPayload, item.source.Size-offset)
				jobs = append(jobs, buildJob{
					spec:    object.BuildSpec{TaskID: task.ID, EncodedSalt: task.Salt, Key: key, MaxObjectSize: task.BlockSize, GroupID: groupID, Part: part + 1, TotalParts: parts, Kind: "file", Slices: []object.Slice{{File: item.source, FileOffset: offset, Length: length}}, CacheDir: cacheDir},
					fileIDs: []string{item.source.ID},
				})
			}
			continue
		}
		if packSize+item.source.Size > maxPayload || len(pack) >= 5000 {
			flushPack()
		}
		pack = append(pack, object.Slice{File: item.source, Length: item.source.Size})
		packSize += item.source.Size
	}
	flushPack()
	for index := range jobs {
		for _, slice := range jobs[index].spec.Slices {
			jobs[index].bytes += slice.Length
		}
		first := jobs[index].spec.Slices[0]
		jobs[index].label = displayPath(first.File.RootAlias, first.File.RelativePath)
		if len(jobs[index].spec.Slices) > 1 {
			jobs[index].label += fmt.Sprintf(" 等%d个文件", len(jobs[index].spec.Slices))
		}
		if jobs[index].spec.TotalParts > 1 {
			jobs[index].label += fmt.Sprintf("（part %d/%d）", jobs[index].spec.Part, jobs[index].spec.TotalParts)
		}
	}
	return jobs, nil
}

func (e *Engine) executeJobs(ctx context.Context, task model.Task, repo *repository.Repository, settings model.GlobalSettings, jobs []buildJob) <-chan jobResult {
	results := make(chan jobResult)
	jobChannel := make(chan buildJob)
	workerCount := settings.UploadConcurrency
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > 3 {
		workerCount = 3
	}
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobChannel {
				if err := e.Control.Wait(ctx); err != nil {
					results <- jobResult{fileIDs: job.fileIDs, label: job.label, bytes: job.bytes, err: err}
					continue
				}
				built, err := object.Build(ctx, job.spec)
				if err == nil {
					err = validateSourcesUnchanged(job.spec.Slices)
				}
				if err == nil {
					err = e.uploadWithRetry(ctx, repo, task.Name, built)
				}
				if built.TempPath != "" {
					_ = os.Remove(built.TempPath)
				}
				results <- jobResult{built: built, fileIDs: job.fileIDs, label: job.label, bytes: job.bytes, err: err}
			}
		}()
	}
	go func() {
		defer close(jobChannel)
		for _, job := range jobs {
			select {
			case jobChannel <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	return results
}

func (e *Engine) uploadWithRetry(ctx context.Context, repo *repository.Repository, taskName string, built object.BuiltObject) error {
	var lastErr error
	for attempt := 0; attempt <= len(e.RetryDelays); attempt++ {
		if attempt > 0 {
			delay := e.RetryDelays[attempt-1]
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		if err := repo.UploadObject(ctx, taskName, built.RemotePath, built.TempPath, built.Record.Size); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("上传 %s 失败: %w", built.RemotePath, lastErr)
}

func (e *Engine) applyRetention(ctx context.Context, task model.Task, repo *repository.Repository) error {
	snapshots, err := e.State.Snapshots(ctx, task.ID)
	if err != nil {
		return err
	}
	retention := task.Retention
	if retention <= 0 {
		retention = model.DefaultRetention
	}
	unlockedKept := 0
	remaining := []model.Snapshot{}
	removed := []model.Snapshot{}
	for _, snapshot := range snapshots {
		if snapshot.ID == "archive" {
			continue
		}
		if snapshot.Locked {
			remaining = append(remaining, snapshot)
			continue
		}
		if unlockedKept < retention {
			unlockedKept++
			remaining = append(remaining, snapshot)
			continue
		}
		removed = append(removed, snapshot)
	}
	if len(removed) == 0 {
		return nil
	}
	referenced := make(map[string]struct{})
	for _, snapshot := range remaining {
		for _, objectRecord := range snapshot.Objects {
			referenced[objectRecord.Path] = struct{}{}
		}
	}
	deleteCandidates := []model.ObjectRecord{}
	for _, snapshot := range removed {
		if err := repo.DeleteSnapshot(ctx, task, snapshot.ID); err != nil {
			return err
		}
		if err := e.State.DeleteSnapshot(ctx, task.ID, snapshot.ID); err != nil {
			return err
		}
		for _, objectRecord := range snapshot.Objects {
			if _, stillUsed := referenced[objectRecord.Path]; !stillUsed {
				deleteCandidates = append(deleteCandidates, objectRecord)
			}
		}
	}
	issues := repo.DeleteObjects(ctx, task.Name, deleteCandidates)
	if len(issues) > 0 {
		return fmt.Errorf("%d个无人引用块删除失败", len(issues))
	}
	return nil
}

func (e *Engine) catalogFromState(ctx context.Context, task model.Task) (model.TaskCatalog, error) {
	catalog := catalogForTask(task)
	snapshots, err := e.State.Snapshots(ctx, task.ID)
	if err != nil {
		return model.TaskCatalog{}, err
	}
	for _, snapshot := range snapshots {
		if snapshot.ID != "archive" {
			catalog.Snapshots = append(catalog.Snapshots, repository.SnapshotSummary(snapshot))
		}
	}
	return catalog, nil
}

func catalogForTask(task model.Task) model.TaskCatalog {
	return model.TaskCatalog{
		FormatVersion: model.FormatVersion, TaskID: task.ID, Name: task.Name,
		Mode: task.Mode, BlockSize: task.BlockSize, Retention: task.Retention,
		Schedule: task.Schedule, Sources: task.Sources, UpdatedAt: time.Now().UTC(),
	}
}

func deriveTaskKey(task model.Task) ([]byte, error) {
	salt, err := cryptox.DecodeSalt(task.Salt)
	if err != nil {
		return nil, err
	}
	return cryptox.DeriveKey(task.Password, salt, cryptox.DefaultKDFParams())
}

func sameMetadata(entry model.FileEntry, file scanner.File) bool {
	return entry.Size == file.Size && entry.Times.Modified.Equal(file.Modified)
}

func changedSinceScan(file scanner.File) bool {
	info, err := os.Stat(file.AbsolutePath)
	return err != nil || info.Size() != file.Size || !info.ModTime().Equal(file.Modified)
}

func validateSourcesUnchanged(slices []object.Slice) error {
	seen := map[string]struct{}{}
	for _, slice := range slices {
		if _, exists := seen[slice.File.AbsolutePath]; exists {
			continue
		}
		seen[slice.File.AbsolutePath] = struct{}{}
		info, err := os.Stat(slice.File.AbsolutePath)
		if err != nil || info.Size() != slice.File.Size || !info.ModTime().Equal(slice.File.Times.Modified) {
			return errSourceChanged
		}
	}
	return nil
}

func fileKey(alias, relative string) string { return alias + "\x00" + filepath.ToSlash(relative) }

func displayPath(alias, relative string) string {
	return filepath.ToSlash(filepath.Join(alias, filepath.FromSlash(relative)))
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func containsEntry(entries []model.FileEntry, key string) bool {
	for _, entry := range entries {
		if fileKey(entry.RootAlias, entry.RelativePath) == key {
			return true
		}
	}
	return false
}

func cloneEntries(entries []model.FileEntry) []model.FileEntry {
	cloned := make([]model.FileEntry, len(entries))
	copy(cloned, entries)
	return cloned
}

func sameSources(left, right []model.SourceRoot) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func blocksCoverFile(blocks []model.BlockRef, size int64) bool {
	if size == 0 {
		return len(blocks) > 0
	}
	var total int64
	for _, block := range blocks {
		total += block.Length
	}
	return total == size
}

func recordsFromEntries(entries []model.FileEntry) []model.ObjectRecord {
	seen := map[string]struct{}{}
	objects := []model.ObjectRecord{}
	for _, entry := range entries {
		if entry.MissingReason != "" {
			continue
		}
		for _, block := range entry.Blocks {
			if _, exists := seen[block.ObjectPath]; exists {
				continue
			}
			seen[block.ObjectPath] = struct{}{}
			objects = append(objects, model.ObjectRecord{
				Path: block.ObjectPath, ID: block.ObjectID, GroupID: block.GroupID,
				Part: block.Part, TotalParts: block.TotalParts,
				Size: block.ObjectSize, Hash: block.ObjectHash,
			})
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Path < objects[j].Path })
	return objects
}

func snapshotID(now time.Time) string {
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
}

func archiveConflictPath(relativePath, hash string) string {
	extension := filepath.Ext(relativePath)
	stem := strings.TrimSuffix(relativePath, extension)
	shortHash := hash
	if len(shortHash) > 8 {
		shortHash = shortHash[:8]
	}
	return fmt.Sprintf("%s (archive-%s)%s", stem, shortHash, extension)
}
