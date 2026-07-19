package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/storage"
	"github.com/google/uuid"
)

const maxIndexBytes = 512 * 1024 * 1024

type Repository struct {
	store storage.Store
}

func New(store storage.Store) *Repository {
	return &Repository{store: store}
}

func ValidateTaskName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return errors.New("task name is required")
	}
	if strings.ContainsAny(name, `/\\`) {
		return errors.New("task name cannot contain path separators")
	}
	return nil
}

func (r *Repository) Initialize(ctx context.Context, task model.Task) (model.TaskCatalog, error) {
	if err := ValidateTaskName(task.Name); err != nil {
		return model.TaskCatalog{}, err
	}
	if err := r.store.MkdirAll(ctx, task.Name); err != nil {
		return model.TaskCatalog{}, err
	}
	descriptor := model.TaskDescriptor{
		FormatVersion: model.FormatVersion, TaskID: task.ID, Name: task.Name,
		Mode: task.Mode, Salt: task.Salt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
	if err := r.saveDescriptor(ctx, task.Name, descriptor); err != nil {
		return model.TaskCatalog{}, err
	}
	catalog := model.TaskCatalog{
		FormatVersion: model.FormatVersion, TaskID: task.ID, Name: task.Name,
		Mode: task.Mode, BlockSize: task.BlockSize, Retention: task.Retention,
		Schedule: task.Schedule, Sources: task.Sources, UpdatedAt: task.UpdatedAt,
	}
	if task.Mode == model.TaskModeArchive {
		catalog.Archive = &model.Snapshot{ID: "archive", TaskID: task.ID, CreatedAt: task.CreatedAt, Complete: true, Sources: task.Sources}
	}
	if err := r.SaveCatalog(ctx, task, catalog); err != nil {
		return model.TaskCatalog{}, err
	}
	return catalog, nil
}

func (r *Repository) saveDescriptor(ctx context.Context, taskName string, descriptor model.TaskDescriptor) error {
	encoded, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return err
	}
	for _, name := range []string{"task-a.json", "task-b.json"} {
		if err := r.atomicPut(ctx, path.Join(taskName, name), encoded); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) LoadDescriptor(ctx context.Context, taskName string) (model.TaskDescriptor, error) {
	var found []model.TaskDescriptor
	for _, name := range []string{"task-a.json", "task-b.json"} {
		encoded, err := r.readAll(ctx, path.Join(taskName, name), 1024*1024)
		if err != nil {
			continue
		}
		var descriptor model.TaskDescriptor
		if json.Unmarshal(encoded, &descriptor) == nil && descriptor.FormatVersion == model.FormatVersion {
			found = append(found, descriptor)
		}
	}
	if len(found) == 0 {
		return model.TaskDescriptor{}, errors.New("both task descriptors are missing or invalid")
	}
	sort.Slice(found, func(i, j int) bool { return found[i].UpdatedAt.After(found[j].UpdatedAt) })
	return found[0], nil
}

func (r *Repository) Discover(ctx context.Context) ([]model.TaskDescriptor, error) {
	items, err := r.store.List(ctx, "")
	if err != nil {
		return nil, err
	}
	descriptors := []model.TaskDescriptor{}
	for _, item := range items {
		if !item.IsDir {
			continue
		}
		descriptor, err := r.LoadDescriptor(ctx, item.Name)
		if err == nil {
			descriptors = append(descriptors, descriptor)
		}
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	return descriptors, nil
}

func (r *Repository) LoadCatalog(ctx context.Context, taskName, password string) (model.TaskDescriptor, model.TaskCatalog, error) {
	descriptor, err := r.LoadDescriptor(ctx, taskName)
	if err != nil {
		return model.TaskDescriptor{}, model.TaskCatalog{}, err
	}
	salt, err := cryptox.DecodeSalt(descriptor.Salt)
	if err != nil {
		return model.TaskDescriptor{}, model.TaskCatalog{}, err
	}
	key, err := cryptox.DeriveKey(password, salt, cryptox.DefaultKDFParams())
	if err != nil {
		return model.TaskDescriptor{}, model.TaskCatalog{}, err
	}
	catalogs := []model.TaskCatalog{}
	for _, name := range []string{"catalog-a.wci", "catalog-b.wci"} {
		var catalog model.TaskCatalog
		if err := r.readEncryptedJSON(ctx, path.Join(taskName, name), key, &catalog); err == nil && catalog.TaskID == descriptor.TaskID {
			catalogs = append(catalogs, catalog)
		}
	}
	if len(catalogs) == 0 {
		return model.TaskDescriptor{}, model.TaskCatalog{}, errors.New("both encrypted task catalogs are missing, invalid, or password is wrong")
	}
	sort.Slice(catalogs, func(i, j int) bool { return catalogs[i].UpdatedAt.After(catalogs[j].UpdatedAt) })
	return descriptor, catalogs[0], nil
}

func (r *Repository) SaveCatalog(ctx context.Context, task model.Task, catalog model.TaskCatalog) error {
	key, err := taskKey(task)
	if err != nil {
		return err
	}
	catalog.UpdatedAt = time.Now().UTC()
	for _, name := range []string{"catalog-a.wci", "catalog-b.wci"} {
		if err := r.writeEncryptedJSON(ctx, task, key, path.Join(task.Name, name), "catalog", catalog); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) SaveSnapshot(ctx context.Context, task model.Task, snapshot model.Snapshot) error {
	key, err := taskKey(task)
	if err != nil {
		return err
	}
	directory := path.Join(task.Name, "snapshots", snapshot.ID)
	if err := r.store.MkdirAll(ctx, directory); err != nil {
		return err
	}
	for _, name := range []string{"index-a.wci", "index-b.wci"} {
		if err := r.writeEncryptedJSON(ctx, task, key, path.Join(directory, name), "snapshot-index", snapshot); err != nil {
			return err
		}
	}
	marker, _ := json.Marshal(map[string]any{"snapshotId": snapshot.ID, "completedAt": time.Now().UTC(), "complete": snapshot.Complete})
	return r.atomicPut(ctx, path.Join(directory, "COMPLETE"), marker)
}

func (r *Repository) LoadSnapshot(ctx context.Context, taskName, snapshotID, password string) (model.Snapshot, error) {
	descriptor, err := r.LoadDescriptor(ctx, taskName)
	if err != nil {
		return model.Snapshot{}, err
	}
	salt, err := cryptox.DecodeSalt(descriptor.Salt)
	if err != nil {
		return model.Snapshot{}, err
	}
	key, err := cryptox.DeriveKey(password, salt, cryptox.DefaultKDFParams())
	if err != nil {
		return model.Snapshot{}, err
	}
	directory := path.Join(taskName, "snapshots", snapshotID)
	for _, name := range []string{"index-a.wci", "index-b.wci"} {
		var snapshot model.Snapshot
		if err := r.readEncryptedJSON(ctx, path.Join(directory, name), key, &snapshot); err == nil && snapshot.TaskID == descriptor.TaskID {
			return snapshot, nil
		}
	}
	return model.Snapshot{}, errors.New("both snapshot indexes are missing or invalid")
}

func (r *Repository) LockSnapshot(ctx context.Context, task model.Task, snapshotID, note string) error {
	content, _ := json.Marshal(map[string]any{"snapshotId": snapshotID, "lockedAt": time.Now().UTC(), "note": note})
	return r.atomicPut(ctx, path.Join(task.Name, "snapshots", snapshotID, "KEEP"), content)
}

func (r *Repository) UnlockSnapshot(ctx context.Context, task model.Task, snapshotID string) error {
	err := r.store.Delete(ctx, path.Join(task.Name, "snapshots", snapshotID, "KEEP"))
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

func (r *Repository) DeleteSnapshot(ctx context.Context, task model.Task, snapshotID string) error {
	return r.store.Delete(ctx, path.Join(task.Name, "snapshots", snapshotID))
}

func (r *Repository) RenameTask(ctx context.Context, oldName, newName string) error {
	if err := ValidateTaskName(newName); err != nil {
		return err
	}
	if _, err := r.store.Stat(ctx, newName); err == nil {
		return errors.New("a remote task with the new name already exists")
	}
	return r.store.Move(ctx, oldName, newName)
}

func (r *Repository) UpdateDescriptor(ctx context.Context, task model.Task) error {
	descriptor := model.TaskDescriptor{
		FormatVersion: model.FormatVersion, TaskID: task.ID, Name: task.Name,
		Mode: task.Mode, Salt: task.Salt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
	return r.saveDescriptor(ctx, task.Name, descriptor)
}

func (r *Repository) DeleteTask(ctx context.Context, taskName string) error {
	return r.store.Delete(ctx, taskName)
}

type CheckIssue struct {
	Path     string `json:"path"`
	Expected int64  `json:"expected,omitempty"`
	Actual   int64  `json:"actual,omitempty"`
	Kind     string `json:"kind"`
}

type CheckResult struct {
	Checked int          `json:"checked"`
	Issues  []CheckIssue `json:"issues"`
}

func (r *Repository) QuickCheck(ctx context.Context, taskName string, objects []model.ObjectRecord) CheckResult {
	return r.QuickCheckReferenced(ctx, taskName, objects, objects)
}

func (r *Repository) QuickCheckReferenced(ctx context.Context, taskName string, objects, allReferenced []model.ObjectRecord) CheckResult {
	result := CheckResult{Issues: []CheckIssue{}}
	jobs := make(chan model.ObjectRecord)
	var mutex sync.Mutex
	var workers sync.WaitGroup
	workerCount := 12
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for object := range jobs {
				info, err := r.store.Stat(ctx, path.Join(taskName, object.Path))
				mutex.Lock()
				result.Checked++
				if errors.Is(err, storage.ErrNotFound) {
					result.Issues = append(result.Issues, CheckIssue{Path: object.Path, Expected: object.Size, Kind: "missing"})
				} else if err != nil {
					result.Issues = append(result.Issues, CheckIssue{Path: object.Path, Expected: object.Size, Kind: "error"})
				} else if info.Size != object.Size {
					result.Issues = append(result.Issues, CheckIssue{Path: object.Path, Expected: object.Size, Actual: info.Size, Kind: "size"})
				}
				mutex.Unlock()
			}
		}()
	}
	for _, object := range uniqueObjects(objects) {
		select {
		case jobs <- object:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return result
		}
	}
	close(jobs)
	workers.Wait()
	referenced := make(map[string]struct{}, len(allReferenced))
	for _, object := range allReferenced {
		referenced[object.Path] = struct{}{}
	}
	remoteFiles, walkErr := r.walkFiles(ctx, path.Join(taskName, "objects"))
	if walkErr != nil && !errors.Is(walkErr, storage.ErrNotFound) {
		result.Issues = append(result.Issues, CheckIssue{Path: "objects", Kind: "list_error"})
	}
	for _, remotePath := range remoteFiles {
		relative := strings.TrimPrefix(strings.TrimPrefix(remotePath, taskName), "/")
		if _, exists := referenced[relative]; !exists {
			result.Issues = append(result.Issues, CheckIssue{Path: relative, Kind: "unreferenced"})
		}
	}
	sort.Slice(result.Issues, func(i, j int) bool { return result.Issues[i].Path < result.Issues[j].Path })
	return result
}

func (r *Repository) walkFiles(ctx context.Context, directory string) ([]string, error) {
	items, err := r.store.List(ctx, directory)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, item := range items {
		if item.IsDir {
			nested, nestedErr := r.walkFiles(ctx, item.Path)
			if nestedErr != nil {
				return nil, nestedErr
			}
			files = append(files, nested...)
			continue
		}
		files = append(files, item.Path)
	}
	return files, nil
}

func (r *Repository) DeleteObjects(ctx context.Context, taskName string, objects []model.ObjectRecord) []CheckIssue {
	issues := []CheckIssue{}
	for _, object := range uniqueObjects(objects) {
		if err := r.store.Delete(ctx, path.Join(taskName, object.Path)); err != nil && !errors.Is(err, storage.ErrNotFound) {
			issues = append(issues, CheckIssue{Path: object.Path, Kind: "delete_error"})
		}
	}
	return issues
}

func (r *Repository) DeletePaths(ctx context.Context, taskName string, objectPaths []string) []CheckIssue {
	issues := []CheckIssue{}
	for _, objectPath := range objectPaths {
		if err := r.store.Delete(ctx, path.Join(taskName, objectPath)); err != nil && !errors.Is(err, storage.ErrNotFound) {
			issues = append(issues, CheckIssue{Path: objectPath, Kind: "delete_error"})
		}
	}
	return issues
}

func (r *Repository) UploadObject(ctx context.Context, taskName, objectPath, localPath string, size int64) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	destination := path.Join(taskName, objectPath)
	temporary := destination + ".partial-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := r.store.Put(ctx, temporary, file, size); err != nil {
		_ = r.store.Delete(context.Background(), temporary)
		return err
	}
	info, err := r.store.Stat(ctx, temporary)
	if err != nil || info.Size != size {
		_ = r.store.Delete(context.Background(), temporary)
		return errors.New("uploaded object size verification failed")
	}
	if err := r.store.Move(ctx, temporary, destination); err != nil {
		_ = r.store.Delete(context.Background(), temporary)
		return err
	}
	return nil
}

func (r *Repository) OpenObject(ctx context.Context, taskName, objectPath string) (io.ReadCloser, error) {
	return r.store.Open(ctx, path.Join(taskName, objectPath))
}

func (r *Repository) StatObject(ctx context.Context, taskName, objectPath string) (storage.Info, error) {
	return r.store.Stat(ctx, path.Join(taskName, objectPath))
}

func (r *Repository) writeEncryptedJSON(ctx context.Context, task model.Task, key []byte, destination, kind string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header, err := cryptox.NewHeader(task.ID, strings.ReplaceAll(uuid.NewString(), "-", ""), kind, task.Salt)
	if err != nil {
		return err
	}
	var encrypted bytes.Buffer
	if _, err := cryptox.EncryptObject(&encrypted, key, header, map[string]string{"kind": kind}, bytes.NewReader(payload)); err != nil {
		return err
	}
	return r.atomicPut(ctx, destination, encrypted.Bytes())
}

func (r *Repository) readEncryptedJSON(ctx context.Context, objectPath string, key []byte, destination any) error {
	reader, err := r.store.Open(ctx, objectPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	objectReader, err := cryptox.OpenObjectWithKey(reader, key)
	if err != nil {
		return err
	}
	payload, err := io.ReadAll(io.LimitReader(objectReader.Payload, maxIndexBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxIndexBytes {
		return errors.New("encrypted index is too large")
	}
	return json.Unmarshal(payload, destination)
}

func (r *Repository) atomicPut(ctx context.Context, destination string, content []byte) error {
	temporary := destination + ".partial-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := r.store.Put(ctx, temporary, bytes.NewReader(content), int64(len(content))); err != nil {
		return err
	}
	info, err := r.store.Stat(ctx, temporary)
	if err != nil || info.Size != int64(len(content)) {
		_ = r.store.Delete(context.Background(), temporary)
		return errors.New("temporary upload size verification failed")
	}
	if err := r.store.Move(ctx, temporary, destination); err != nil {
		_ = r.store.Delete(context.Background(), temporary)
		return err
	}
	return nil
}

func (r *Repository) readAll(ctx context.Context, objectPath string, maximum int64) ([]byte, error) {
	reader, err := r.store.Open(ctx, objectPath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("remote file exceeds maximum size")
	}
	return content, nil
}

func taskKey(task model.Task) ([]byte, error) {
	salt, err := cryptox.DecodeSalt(task.Salt)
	if err != nil {
		return nil, err
	}
	return cryptox.DeriveKey(task.Password, salt, cryptox.DefaultKDFParams())
}

func uniqueObjects(objects []model.ObjectRecord) []model.ObjectRecord {
	seen := make(map[string]struct{}, len(objects))
	unique := make([]model.ObjectRecord, 0, len(objects))
	for _, object := range objects {
		if _, exists := seen[object.Path]; exists {
			continue
		}
		seen[object.Path] = struct{}{}
		unique = append(unique, object)
	}
	return unique
}

func SnapshotSummary(snapshot model.Snapshot) model.SnapshotSummary {
	var size int64
	for _, file := range snapshot.Files {
		size += file.Size
	}
	return model.SnapshotSummary{
		ID: snapshot.ID, CreatedAt: snapshot.CreatedAt, Complete: snapshot.Complete,
		Locked: snapshot.Locked, FileCount: len(snapshot.Files), Size: size,
	}
}
