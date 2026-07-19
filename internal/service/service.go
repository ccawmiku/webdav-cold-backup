package service

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

	"github.com/ccawmiku/webdav-cold-backup/internal/backup"
	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/repository"
	"github.com/ccawmiku/webdav-cold-backup/internal/restore"
	"github.com/ccawmiku/webdav-cold-backup/internal/scanner"
	"github.com/ccawmiku/webdav-cold-backup/internal/state"
	"github.com/ccawmiku/webdav-cold-backup/internal/storage"
	"github.com/google/uuid"
)

var supportedBlockSizes = map[int64]struct{}{
	1_000_000_000: {},
	2_000_000_000: {},
	3_700_000_000: {},
}

type Config struct {
	ConfigDir   string
	CacheDir    string
	SourceRoot  string
	RestoreRoot string
}

type Service struct {
	config  Config
	state   *state.Store
	engine  *backup.Engine
	control *backup.Control
	queue   chan string
	ctx     context.Context
	cancel  context.CancelFunc
	mutex   sync.Mutex
	queued  map[string]bool
	current string
}

func New(config Config) (*Service, error) {
	store, err := state.Open(config.ConfigDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.CacheDir, 0o700); err != nil {
		_ = store.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	control := backup.NewControl()
	service := &Service{
		config: config, state: store, control: control,
		engine: backup.NewEngine(store, config.CacheDir, control),
		queue:  make(chan string, 100), ctx: ctx, cancel: cancel, queued: map[string]bool{},
	}
	go service.worker()
	go service.scheduler()
	return service, nil
}

func (s *Service) Close() error {
	s.cancel()
	return s.state.Close()
}

func (s *Service) State() *state.Store { return s.state }

type CreateTaskInput struct {
	Name      string             `json:"name"`
	Mode      model.TaskMode     `json:"mode"`
	Password  string             `json:"password"`
	Sources   []model.SourceRoot `json:"sources"`
	Remote    model.WebDAVConfig `json:"remote"`
	BlockSize int64              `json:"blockSize"`
	Retention int                `json:"retention"`
	Schedule  model.Schedule     `json:"schedule"`
}

func (s *Service) CreateTask(ctx context.Context, input CreateTaskInput) (model.PublicTask, error) {
	if err := repository.ValidateTaskName(input.Name); err != nil {
		return model.PublicTask{}, err
	}
	if input.Mode != model.TaskModeSnapshot && input.Mode != model.TaskModeArchive {
		return model.PublicTask{}, errors.New("任务模式无效")
	}
	if input.Password == "" {
		return model.PublicTask{}, errors.New("任务密码不能为空")
	}
	if _, ok := supportedBlockSizes[input.BlockSize]; !ok {
		return model.PublicTask{}, errors.New("块容量只能选择1GB、2GB或3.7GB")
	}
	input.Sources = normalizeAliases(input.Sources)
	if err := scanner.ValidateSources(input.Sources); err != nil {
		return model.PublicTask{}, err
	}
	if err := s.validateMappedSources(input.Sources); err != nil {
		return model.PublicTask{}, err
	}
	if input.Retention <= 0 {
		input.Retention = model.DefaultRetention
	}
	if err := validateSchedule(input.Schedule); err != nil {
		return model.PublicTask{}, err
	}
	salt, err := cryptox.RandomSalt()
	if err != nil {
		return model.PublicTask{}, err
	}
	now := time.Now().UTC()
	task := model.Task{
		ID: strings.ReplaceAll(uuid.NewString(), "-", ""), Name: input.Name,
		Mode: input.Mode, Password: input.Password, Salt: cryptox.EncodeSalt(salt),
		Sources: input.Sources, Remote: input.Remote, BlockSize: input.BlockSize,
		Retention: input.Retention, Schedule: input.Schedule, Status: model.TaskIdle,
		CreatedAt: now, UpdatedAt: now, AttachedWritable: true,
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return model.PublicTask{}, err
	}
	if err := s.testRemote(ctx, task); err != nil {
		return model.PublicTask{}, err
	}
	if descriptors, discoverErr := repo.Discover(ctx); discoverErr == nil {
		for _, descriptor := range descriptors {
			if strings.EqualFold(descriptor.Name, task.Name) {
				return model.PublicTask{}, errors.New("远端已经存在同名任务")
			}
		}
	}
	if _, err := repo.Initialize(ctx, task); err != nil {
		return model.PublicTask{}, err
	}
	if err := s.state.SaveTask(ctx, task); err != nil {
		return model.PublicTask{}, err
	}
	return task.Public(), nil
}

func (s *Service) Tasks(ctx context.Context) ([]model.PublicTask, error) {
	tasks, err := s.state.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	public := make([]model.PublicTask, 0, len(tasks))
	for _, task := range tasks {
		public = append(public, task.Public())
	}
	return public, nil
}

func (s *Service) Task(ctx context.Context, id string) (model.Task, error) {
	return s.state.Task(ctx, id)
}

type UpdateTaskInput struct {
	Name      string             `json:"name"`
	Sources   []model.SourceRoot `json:"sources"`
	Retention int                `json:"retention"`
	Schedule  model.Schedule     `json:"schedule"`
}

func (s *Service) UpdateTask(ctx context.Context, id string, input UpdateTaskInput) (model.PublicTask, error) {
	task, err := s.state.Task(ctx, id)
	if err != nil {
		return model.PublicTask{}, err
	}
	if err := repository.ValidateTaskName(input.Name); err != nil {
		return model.PublicTask{}, err
	}
	input.Sources = normalizeAliases(input.Sources)
	if err := scanner.ValidateSources(input.Sources); err != nil {
		return model.PublicTask{}, err
	}
	if err := s.validateMappedSources(input.Sources); err != nil {
		return model.PublicTask{}, err
	}
	if err := validateSchedule(input.Schedule); err != nil {
		return model.PublicTask{}, err
	}
	if input.Retention <= 0 {
		input.Retention = model.DefaultRetention
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return model.PublicTask{}, err
	}
	original := task
	oldName := task.Name
	renamed := input.Name != oldName
	if input.Name != oldName {
		if err := repo.RenameTask(ctx, oldName, input.Name); err != nil {
			return model.PublicTask{}, err
		}
		task.Name = input.Name
	}
	task.Sources = input.Sources
	task.Retention = input.Retention
	task.Schedule = input.Schedule
	task.UpdatedAt = time.Now().UTC()
	rollbackRemote := func() {
		rollbackContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if renamed {
			_ = repo.RenameTask(rollbackContext, task.Name, original.Name)
		}
		_ = repo.UpdateDescriptor(rollbackContext, original)
		if originalCatalog, catalogErr := s.engineCatalog(rollbackContext, original); catalogErr == nil {
			_ = repo.SaveCatalog(rollbackContext, original, originalCatalog)
		}
	}
	if err := repo.UpdateDescriptor(ctx, task); err != nil {
		rollbackRemote()
		return model.PublicTask{}, err
	}
	catalog, err := s.engineCatalog(ctx, task)
	if err != nil {
		rollbackRemote()
		return model.PublicTask{}, err
	}
	if err := repo.SaveCatalog(ctx, task, catalog); err != nil {
		rollbackRemote()
		return model.PublicTask{}, err
	}
	if err := s.state.SaveTask(ctx, task); err != nil {
		rollbackRemote()
		return model.PublicTask{}, err
	}
	return task.Public(), nil
}

func (s *Service) DeleteTask(ctx context.Context, id, password, confirmName string) error {
	task, err := s.state.Task(ctx, id)
	if err != nil {
		return err
	}
	if password != task.Password || confirmName != task.Name {
		return errors.New("任务密码或确认名称不正确")
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return err
	}
	if err := repo.DeleteTask(ctx, task.Name); err != nil {
		return err
	}
	return s.state.DeleteTask(ctx, id)
}

func (s *Service) Enqueue(ctx context.Context, id string) error {
	task, err := s.state.Task(ctx, id)
	if err != nil {
		return err
	}
	if !task.AttachedWritable {
		return errors.New("任务当前为只读状态，请先确认远端接管")
	}
	s.mutex.Lock()
	if s.queued[id] || s.current == id {
		s.mutex.Unlock()
		return errors.New("任务已经在队列或正在运行")
	}
	s.queued[id] = true
	s.mutex.Unlock()
	task.Status = model.TaskQueued
	task.UpdatedAt = time.Now().UTC()
	_ = s.state.SaveTask(ctx, task)
	select {
	case s.queue <- id:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Pause(ctx context.Context, id string) error {
	s.mutex.Lock()
	current := s.current
	s.mutex.Unlock()
	if current != id {
		return errors.New("任务当前没有运行")
	}
	s.control.Pause()
	task, err := s.state.Task(ctx, id)
	if err == nil {
		task.Status = model.TaskPaused
		task.UpdatedAt = time.Now().UTC()
		err = s.state.SaveTask(ctx, task)
	}
	return err
}

func (s *Service) Resume(ctx context.Context, id string) error {
	s.mutex.Lock()
	current := s.current
	s.mutex.Unlock()
	if current != id {
		return errors.New("任务当前没有暂停")
	}
	s.control.Resume()
	task, err := s.state.Task(ctx, id)
	if err == nil {
		task.Status = model.TaskRunning
		task.UpdatedAt = time.Now().UTC()
		err = s.state.SaveTask(ctx, task)
	}
	return err
}

func (s *Service) worker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case id := <-s.queue:
			s.mutex.Lock()
			delete(s.queued, id)
			s.current = id
			s.mutex.Unlock()
			s.control.Resume()
			task, err := s.state.Task(s.ctx, id)
			if err == nil {
				task.Status = model.TaskRunning
				task.UpdatedAt = time.Now().UTC()
				_ = s.state.SaveTask(s.ctx, task)
				settings, _ := s.state.Settings(s.ctx)
				repo, repoErr := s.repositoryFor(task)
				if repoErr == nil {
					run, runErr := s.engine.Run(s.ctx, task, repo, settings)
					if runErr != nil || run.Status == model.RunFailed {
						latest, loadErr := s.state.Task(context.Background(), task.ID)
						if loadErr == nil {
							latest.Status = model.TaskFailed
							latest.UpdatedAt = time.Now().UTC()
							_ = s.state.SaveTask(context.Background(), latest)
						}
					}
				} else {
					task.Status = model.TaskFailed
					task.UpdatedAt = time.Now().UTC()
					_ = s.state.SaveTask(context.Background(), task)
				}
			}
			s.mutex.Lock()
			s.current = ""
			s.mutex.Unlock()
		}
	}
}

func (s *Service) scheduler() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scheduleDueTasks()
		}
	}
}

func (s *Service) scheduleDueTasks() {
	settings, err := s.state.Settings(s.ctx)
	if err != nil {
		return
	}
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		location = time.UTC
	}
	now := time.Now().In(location)
	tasks, err := s.state.Tasks(s.ctx)
	if err != nil {
		return
	}
	for _, task := range tasks {
		key, due := scheduleKey(task.Schedule, now)
		if !due || task.LastScheduleKey == key {
			continue
		}
		task.LastScheduleKey = key
		task.UpdatedAt = time.Now().UTC()
		_ = s.state.SaveTask(s.ctx, task)
		_ = s.Enqueue(s.ctx, task.ID)
	}
}

type AttachInput struct {
	Remote   model.WebDAVConfig `json:"remote"`
	TaskName string             `json:"taskName"`
	Password string             `json:"password"`
	Sources  []model.SourceRoot `json:"sources"`
}

type AttachResult struct {
	Task        model.PublicTask       `json:"task"`
	Differences []string               `json:"differences"`
	Check       repository.CheckResult `json:"check"`
	Writable    bool                   `json:"writable"`
}

func (s *Service) Discover(ctx context.Context, remote model.WebDAVConfig) ([]model.TaskDescriptor, error) {
	task := model.Task{Remote: remote}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return nil, err
	}
	return repo.Discover(ctx)
}

func (s *Service) Attach(ctx context.Context, input AttachInput) (AttachResult, error) {
	placeholder := model.Task{Remote: input.Remote}
	repo, err := s.repositoryFor(placeholder)
	if err != nil {
		return AttachResult{}, err
	}
	descriptor, catalog, err := repo.LoadCatalog(ctx, input.TaskName, input.Password)
	if err != nil {
		return AttachResult{}, err
	}
	sources := input.Sources
	if len(sources) == 0 {
		sources = catalog.Sources
	}
	sources = normalizeAliases(sources)
	if err := scanner.ValidateSources(sources); err != nil {
		return AttachResult{}, err
	}
	if err := s.validateMappedSources(sources); err != nil {
		return AttachResult{}, err
	}
	now := time.Now().UTC()
	task := model.Task{
		ID: descriptor.TaskID, Name: descriptor.Name, Mode: descriptor.Mode,
		Password: input.Password, Salt: descriptor.Salt, Sources: sources,
		Remote: input.Remote, BlockSize: catalog.BlockSize, Retention: catalog.Retention,
		Schedule: catalog.Schedule, Status: model.TaskReadOnly, CreatedAt: descriptor.CreatedAt,
		UpdatedAt: now, AttachedWritable: false,
	}
	result := AttachResult{}
	var latest *model.Snapshot
	var fallback *model.Snapshot
	if task.Mode == model.TaskModeArchive && catalog.Archive != nil {
		archive := *catalog.Archive
		latest = &archive
		_ = s.state.SaveTask(ctx, task)
		_ = s.state.SaveSnapshot(ctx, archive)
	} else {
		for _, summary := range catalog.Snapshots {
			snapshot, loadErr := repo.LoadSnapshot(ctx, task.Name, summary.ID, task.Password)
			if loadErr != nil {
				continue
			}
			_ = s.state.SaveTask(ctx, task)
			_ = s.state.SaveSnapshot(ctx, snapshot)
			if fallback == nil {
				copySnapshot := snapshot
				fallback = &copySnapshot
			}
			if latest == nil && snapshot.Complete {
				copySnapshot := snapshot
				latest = &copySnapshot
			}
		}
		if latest == nil {
			latest = fallback
		}
	}
	if latest != nil {
		result.Differences = matchSources(*latest, sources)
		result.Check = repo.QuickCheckReferenced(ctx, task.Name, latest.Objects, s.allTaskObjects(ctx, task.ID))
	}
	result.Writable = len(result.Differences) == 0 && len(result.Check.Issues) == 0
	task.AttachedWritable = result.Writable
	if result.Writable {
		task.Status = model.TaskIdle
	}
	if err := s.state.SaveTask(ctx, task); err != nil {
		return AttachResult{}, err
	}
	result.Task = task.Public()
	return result, nil
}

func (s *Service) Reconnect(ctx context.Context, id string, remote model.WebDAVConfig, confirmWrite bool) (AttachResult, error) {
	task, err := s.state.Task(ctx, id)
	if err != nil {
		return AttachResult{}, err
	}
	if remote.Password == "" {
		remote.Password = task.Remote.Password
	}
	candidate := task
	candidate.Remote = remote
	repo, err := s.repositoryFor(candidate)
	if err != nil {
		return AttachResult{}, err
	}
	descriptor, _, err := repo.LoadCatalog(ctx, task.Name, task.Password)
	if err != nil {
		return AttachResult{}, err
	}
	if descriptor.TaskID != task.ID {
		return AttachResult{}, errors.New("远端任务UUID与本地任务不一致")
	}
	var latest model.Snapshot
	hasLatest := false
	items, _ := s.state.Snapshots(ctx, task.ID)
	if len(items) > 0 {
		latest = items[0]
		hasLatest = true
	}
	check := repository.CheckResult{}
	if hasLatest {
		check = repo.QuickCheckReferenced(ctx, task.Name, latest.Objects, s.allTaskObjects(ctx, task.ID))
	}
	task.Remote = remote
	task.AttachedWritable = confirmWrite && len(check.Issues) == 0
	if task.AttachedWritable {
		task.Status = model.TaskIdle
	} else {
		task.Status = model.TaskReadOnly
	}
	task.UpdatedAt = time.Now().UTC()
	if err := s.state.SaveTask(ctx, task); err != nil {
		return AttachResult{}, err
	}
	return AttachResult{Task: task.Public(), Check: check, Writable: task.AttachedWritable}, nil
}

func (s *Service) QuickCheck(ctx context.Context, id, snapshotID string) (repository.CheckResult, error) {
	task, err := s.state.Task(ctx, id)
	if err != nil {
		return repository.CheckResult{}, err
	}
	snapshot, err := s.pickSnapshot(ctx, task, snapshotID)
	if err != nil {
		return repository.CheckResult{}, err
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return repository.CheckResult{}, err
	}
	return repo.QuickCheckReferenced(ctx, task.Name, snapshot.Objects, s.allTaskObjects(ctx, task.ID)), nil
}

func (s *Service) CleanupUnreferenced(ctx context.Context, taskID, password string) (int, error) {
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return 0, err
	}
	if password != task.Password {
		return 0, errors.New("任务密码错误")
	}
	objects := s.allTaskObjects(ctx, task.ID)
	repo, err := s.repositoryFor(task)
	if err != nil {
		return 0, err
	}
	check := repo.QuickCheckReferenced(ctx, task.Name, objects, objects)
	paths := []string{}
	for _, issue := range check.Issues {
		if issue.Kind == "unreferenced" {
			paths = append(paths, issue.Path)
		}
	}
	if issues := repo.DeletePaths(ctx, task.Name, paths); len(issues) > 0 {
		return len(paths) - len(issues), fmt.Errorf("%d个未引用对象删除失败", len(issues))
	}
	return len(paths), nil
}

func (s *Service) LockSnapshot(ctx context.Context, taskID, snapshotID, password, note string, locked bool) error {
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if password != task.Password {
		return errors.New("任务密码错误")
	}
	snapshot, err := s.state.Snapshot(ctx, taskID, snapshotID)
	if err != nil {
		return err
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return err
	}
	if locked {
		err = repo.LockSnapshot(ctx, task, snapshotID, note)
	} else {
		err = repo.UnlockSnapshot(ctx, task, snapshotID)
	}
	if err != nil {
		return err
	}
	snapshot.Locked = locked
	snapshot.LockNote = note
	if !locked {
		snapshot.LockNote = ""
	}
	if err := s.state.SaveSnapshot(ctx, snapshot); err != nil {
		return err
	}
	catalog, err := s.engineCatalog(ctx, task)
	if err != nil {
		return err
	}
	return repo.SaveCatalog(ctx, task, catalog)
}

func (s *Service) DeleteSnapshot(ctx context.Context, taskID, snapshotID, password string) error {
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if password != task.Password {
		return errors.New("任务密码错误")
	}
	snapshot, err := s.state.Snapshot(ctx, taskID, snapshotID)
	if err != nil {
		return err
	}
	if snapshot.Locked {
		return errors.New("永久快照必须先解锁")
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return err
	}
	if err := repo.DeleteSnapshot(ctx, task, snapshotID); err != nil {
		return err
	}
	if err := s.state.DeleteSnapshot(ctx, taskID, snapshotID); err != nil {
		return err
	}
	remaining, _ := s.state.Snapshots(ctx, taskID)
	referenced := map[string]struct{}{}
	for _, item := range remaining {
		for _, objectRecord := range item.Objects {
			referenced[objectRecord.Path] = struct{}{}
		}
	}
	remove := []model.ObjectRecord{}
	for _, objectRecord := range snapshot.Objects {
		if _, exists := referenced[objectRecord.Path]; !exists {
			remove = append(remove, objectRecord)
		}
	}
	if issues := repo.DeleteObjects(ctx, task.Name, remove); len(issues) > 0 {
		return fmt.Errorf("%d个无人引用块删除失败", len(issues))
	}
	catalog, err := s.engineCatalog(ctx, task)
	if err != nil {
		return err
	}
	return repo.SaveCatalog(ctx, task, catalog)
}

func (s *Service) DeleteArchiveFiles(ctx context.Context, taskID, password string, fileIDs []string) error {
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Mode != model.TaskModeArchive || password != task.Password {
		return errors.New("任务模式或密码不正确")
	}
	archive, err := s.state.Snapshot(ctx, taskID, "archive")
	if err != nil {
		return err
	}
	removeSet := map[string]struct{}{}
	for _, id := range fileIDs {
		removeSet[id] = struct{}{}
	}
	removedObjects := []model.ObjectRecord{}
	remainingFiles := []model.FileEntry{}
	for _, file := range archive.Files {
		if _, remove := removeSet[file.ID]; remove {
			removedObjects = append(removedObjects, recordsFromBlocks(file.Blocks)...)
			continue
		}
		remainingFiles = append(remainingFiles, file)
	}
	archive.Files = remainingFiles
	archive.Objects = recordsFromFiles(remainingFiles)
	if err := s.state.SaveSnapshot(ctx, archive); err != nil {
		return err
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return err
	}
	catalog := catalogFor(task)
	catalog.Archive = &archive
	if err := repo.SaveCatalog(ctx, task, catalog); err != nil {
		return err
	}
	referenced := map[string]struct{}{}
	for _, objectRecord := range archive.Objects {
		referenced[objectRecord.Path] = struct{}{}
	}
	deleteObjects := []model.ObjectRecord{}
	for _, objectRecord := range removedObjects {
		if _, exists := referenced[objectRecord.Path]; !exists {
			deleteObjects = append(deleteObjects, objectRecord)
		}
	}
	issues := repo.DeleteObjects(ctx, task.Name, deleteObjects)
	if len(issues) > 0 {
		return fmt.Errorf("%d个归档块删除失败", len(issues))
	}
	return nil
}

func (s *Service) Restore(ctx context.Context, taskID, snapshotID string, selected []string, output string) (restore.Report, error) {
	if !pathWithinRoot(output, s.config.RestoreRoot) {
		return restore.Report{}, errors.New("恢复目录必须位于已配置的恢复映射内")
	}
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return restore.Report{}, err
	}
	snapshot, err := s.pickSnapshot(ctx, task, snapshotID)
	if err != nil {
		return restore.Report{}, err
	}
	repo, err := s.repositoryFor(task)
	if err != nil {
		return restore.Report{}, err
	}
	engine := restore.Engine{Repository: repo}
	report, err := engine.Restore(ctx, task, snapshot, selected, output)
	if err == nil {
		_, _, _ = restore.WriteReport(output, report)
	}
	return report, err
}

// RestoreImported restores from a task directory that was downloaded manually.
// The selected directory must be the task root and retain the objects/... layout.
func (s *Service) RestoreImported(ctx context.Context, taskID, snapshotID string, selected []string, taskDirectory, output string) (restore.Report, error) {
	if !pathWithinRoot(output, s.config.RestoreRoot) {
		return restore.Report{}, errors.New("恢复目录必须位于已配置的恢复映射内")
	}
	if !pathWithinRoot(taskDirectory, s.config.SourceRoot) && !pathWithinRoot(taskDirectory, s.config.RestoreRoot) {
		return restore.Report{}, errors.New("已下载任务目录必须位于源或恢复映射内")
	}
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return restore.Report{}, err
	}
	snapshot, err := s.pickSnapshot(ctx, task, snapshotID)
	if err != nil {
		return restore.Report{}, err
	}
	cleanDirectory, err := filepath.Abs(taskDirectory)
	if err != nil {
		return restore.Report{}, fmt.Errorf("resolve imported task directory: %w", err)
	}
	info, err := os.Stat(cleanDirectory)
	if err != nil {
		return restore.Report{}, fmt.Errorf("open imported task directory: %w", err)
	}
	if !info.IsDir() {
		return restore.Report{}, errors.New("imported task path is not a directory")
	}
	if _, err := os.Stat(filepath.Join(cleanDirectory, "objects")); err != nil {
		return restore.Report{}, errors.New("selected directory does not contain an objects directory")
	}
	store, err := storage.NewFileStore(filepath.Dir(cleanDirectory))
	if err != nil {
		return restore.Report{}, err
	}
	importedTask := task
	importedTask.Name = filepath.Base(cleanDirectory)
	engine := restore.Engine{Repository: repository.New(store)}
	report, err := engine.Restore(ctx, importedTask, snapshot, selected, output)
	if err == nil {
		_, _, _ = restore.WriteReport(output, report)
	}
	return report, err
}

func (s *Service) BuildPlan(ctx context.Context, taskID, snapshotID string, selected []string) (restore.Plan, error) {
	task, err := s.state.Task(ctx, taskID)
	if err != nil {
		return restore.Plan{}, err
	}
	snapshot, err := s.pickSnapshot(ctx, task, snapshotID)
	if err != nil {
		return restore.Plan{}, err
	}
	return (&restore.Engine{}).BuildPlan(task, snapshot, selected), nil
}

func (s *Service) Snapshots(ctx context.Context, taskID string) ([]model.Snapshot, error) {
	return s.state.Snapshots(ctx, taskID)
}

func (s *Service) Runs(ctx context.Context, taskID string) ([]model.RunRecord, error) {
	return s.state.Runs(ctx, taskID, 100)
}

func (s *Service) Settings(ctx context.Context) (model.GlobalSettings, error) {
	return s.state.Settings(ctx)
}

func (s *Service) SaveSettings(ctx context.Context, settings model.GlobalSettings) error {
	if settings.UploadConcurrency < 1 || settings.UploadConcurrency > 3 {
		return errors.New("上传并发数必须是1到3")
	}
	if settings.UploadLimitMiB < 0 || settings.DownloadLimitMiB < 0 {
		return errors.New("限速不能为负数")
	}
	if _, err := time.LoadLocation(settings.Timezone); err != nil {
		return errors.New("时区无效")
	}
	return s.state.SaveSettings(ctx, settings)
}

func (s *Service) repositoryFor(task model.Task) (*repository.Repository, error) {
	store, err := storage.NewWebDAVStore(task.Remote.Endpoint, task.Remote.Root, task.Remote.Username, task.Remote.Password, nil)
	if err != nil {
		return nil, err
	}
	settings, settingsErr := s.state.Settings(context.Background())
	if settingsErr == nil {
		store.SetLimits(settings.UploadLimitMiB, settings.DownloadLimitMiB)
	}
	return repository.New(store), nil
}

func (s *Service) testRemote(ctx context.Context, task model.Task) error {
	store, err := storage.NewWebDAVStore(task.Remote.Endpoint, task.Remote.Root, task.Remote.Username, task.Remote.Password, nil)
	if err != nil {
		return err
	}
	return store.TestCompatibility(ctx)
}

func (s *Service) pickSnapshot(ctx context.Context, task model.Task, id string) (model.Snapshot, error) {
	if task.Mode == model.TaskModeArchive {
		return s.state.Snapshot(ctx, task.ID, "archive")
	}
	if id != "" {
		return s.state.Snapshot(ctx, task.ID, id)
	}
	items, err := s.state.Snapshots(ctx, task.ID)
	if err != nil {
		return model.Snapshot{}, err
	}
	for _, item := range items {
		if item.Complete {
			return item, nil
		}
	}
	if len(items) > 0 {
		return items[0], nil
	}
	return model.Snapshot{}, os.ErrNotExist
}

func (s *Service) engineCatalog(ctx context.Context, task model.Task) (model.TaskCatalog, error) {
	catalog := catalogFor(task)
	snapshots, err := s.state.Snapshots(ctx, task.ID)
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

func (s *Service) allTaskObjects(ctx context.Context, taskID string) []model.ObjectRecord {
	snapshots, err := s.state.Snapshots(ctx, taskID)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	objects := []model.ObjectRecord{}
	for _, snapshot := range snapshots {
		for _, objectRecord := range snapshot.Objects {
			if _, exists := seen[objectRecord.Path]; exists {
				continue
			}
			seen[objectRecord.Path] = struct{}{}
			objects = append(objects, objectRecord)
		}
	}
	return objects
}

func catalogFor(task model.Task) model.TaskCatalog {
	return model.TaskCatalog{FormatVersion: model.FormatVersion, TaskID: task.ID, Name: task.Name, Mode: task.Mode, BlockSize: task.BlockSize, Retention: task.Retention, Schedule: task.Schedule, Sources: task.Sources, UpdatedAt: time.Now().UTC()}
}

func normalizeAliases(sources []model.SourceRoot) []model.SourceRoot {
	used := map[string]int{}
	normalized := make([]model.SourceRoot, len(sources))
	for index, source := range sources {
		alias := strings.TrimSpace(source.Alias)
		if alias == "" {
			alias = filepath.Base(filepath.Clean(source.Path))
			if alias == "." || alias == string(filepath.Separator) || alias == "" {
				alias = "source"
			}
		}
		key := strings.ToLower(alias)
		used[key]++
		if used[key] > 1 {
			alias = fmt.Sprintf("%s-%d", alias, used[key])
		}
		normalized[index] = model.SourceRoot{Path: filepath.Clean(source.Path), Alias: alias}
	}
	return normalized
}

func validateSchedule(schedule model.Schedule) error {
	if schedule.Type != model.ScheduleManual && schedule.Type != model.ScheduleDaily && schedule.Type != model.ScheduleWeekly {
		return errors.New("计划类型无效")
	}
	if schedule.Hour < 0 || schedule.Hour > 23 || schedule.Minute < 0 || schedule.Minute > 59 {
		return errors.New("计划时间无效")
	}
	if schedule.Type == model.ScheduleWeekly && (schedule.Weekday < time.Sunday || schedule.Weekday > time.Saturday) {
		return errors.New("每周计划的星期无效")
	}
	return nil
}

func (s *Service) validateMappedSources(sources []model.SourceRoot) error {
	for _, source := range sources {
		if !pathWithinRoot(source.Path, s.config.SourceRoot) {
			return fmt.Errorf("源目录 %q 不在已配置的源映射内", source.Path)
		}
	}
	return nil
}

func pathWithinRoot(candidate, root string) bool {
	if strings.TrimSpace(candidate) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	absoluteCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteCandidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func scheduleKey(schedule model.Schedule, now time.Time) (string, bool) {
	if schedule.Type == model.ScheduleManual || now.Hour() != schedule.Hour || now.Minute() != schedule.Minute {
		return "", false
	}
	if schedule.Type == model.ScheduleWeekly {
		if now.Weekday() != schedule.Weekday {
			return "", false
		}
		year, week := now.ISOWeek()
		return fmt.Sprintf("weekly-%04d-%02d", year, week), true
	}
	return "daily-" + now.Format("2006-01-02"), true
}

func matchSources(snapshot model.Snapshot, sources []model.SourceRoot) []string {
	byAlias := map[string]string{}
	for _, source := range sources {
		byAlias[source.Alias] = source.Path
	}
	differences := []string{}
	for _, file := range snapshot.Files {
		root, exists := byAlias[file.RootAlias]
		if !exists {
			differences = append(differences, "未映射源根: "+file.RootAlias)
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		info, err := os.Stat(path)
		if err != nil {
			differences = append(differences, "缺少: "+filepath.ToSlash(filepath.Join(file.RootAlias, file.RelativePath)))
			continue
		}
		if info.Size() != file.Size {
			differences = append(differences, "大小不同: "+filepath.ToSlash(filepath.Join(file.RootAlias, file.RelativePath)))
		}
	}
	sort.Strings(differences)
	return differences
}

func recordsFromBlocks(blocks []model.BlockRef) []model.ObjectRecord {
	items := []model.ObjectRecord{}
	for _, block := range blocks {
		items = append(items, model.ObjectRecord{Path: block.ObjectPath, ID: block.ObjectID, GroupID: block.GroupID, Part: block.Part, TotalParts: block.TotalParts, Size: block.ObjectSize, Hash: block.ObjectHash})
	}
	return items
}

func recordsFromFiles(files []model.FileEntry) []model.ObjectRecord {
	seen := map[string]struct{}{}
	items := []model.ObjectRecord{}
	for _, file := range files {
		for _, record := range recordsFromBlocks(file.Blocks) {
			if _, exists := seen[record.Path]; exists {
				continue
			}
			seen[record.Path] = struct{}{}
			items = append(items, record)
		}
	}
	return items
}
