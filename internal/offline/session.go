package offline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/ccawmiku/webdav-cold-backup/internal/repository"
	"github.com/ccawmiku/webdav-cold-backup/internal/restore"
	"github.com/ccawmiku/webdav-cold-backup/internal/storage"
)

type Session struct {
	mutex      sync.RWMutex
	taskDir    string
	task       model.Task
	catalog    model.TaskCatalog
	snapshots  map[string]model.Snapshot
	selectedID string
	repository *repository.Repository
	salvaged   bool
}

type OpenResult struct {
	Task      model.PublicTask        `json:"task"`
	Snapshots []model.SnapshotSummary `json:"snapshots"`
	Selected  string                  `json:"selected"`
	Salvaged  bool                    `json:"salvaged"`
}

func (s *Session) Open(ctx context.Context, taskDirectory, password string) (OpenResult, error) {
	absolute, err := filepath.Abs(taskDirectory)
	if err != nil {
		return OpenResult{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return OpenResult{}, errors.New("请选择完整下载的任务目录")
	}
	parent := filepath.Dir(absolute)
	taskName := filepath.Base(absolute)
	fileStore, err := storage.NewFileStore(parent)
	if err != nil {
		return OpenResult{}, err
	}
	repo := repository.New(fileStore)
	descriptor, catalog, loadErr := repo.LoadCatalog(ctx, taskName, password)
	if loadErr != nil {
		task, snapshot, salvageErr := Salvage(ctx, absolute, password)
		if salvageErr != nil {
			return OpenResult{}, fmt.Errorf("索引读取失败（%v），自恢复也失败: %w", loadErr, salvageErr)
		}
		s.mutex.Lock()
		s.taskDir = absolute
		s.task = task
		s.catalog = model.TaskCatalog{FormatVersion: model.FormatVersion, TaskID: task.ID, Name: task.Name, Mode: model.TaskModeArchive, Archive: &snapshot}
		s.snapshots = map[string]model.Snapshot{snapshot.ID: snapshot}
		s.selectedID = snapshot.ID
		s.repository = repo
		s.salvaged = true
		s.mutex.Unlock()
		return s.result(), nil
	}
	task := model.Task{
		ID: descriptor.TaskID, Name: descriptor.Name, Mode: descriptor.Mode,
		Password: password, Salt: descriptor.Salt, Sources: catalog.Sources,
		BlockSize: catalog.BlockSize, Retention: catalog.Retention, Schedule: catalog.Schedule,
		Status: model.TaskReadOnly, CreatedAt: descriptor.CreatedAt, UpdatedAt: descriptor.UpdatedAt,
	}
	snapshots := map[string]model.Snapshot{}
	selected := ""
	if catalog.Archive != nil {
		snapshots[catalog.Archive.ID] = *catalog.Archive
		selected = catalog.Archive.ID
	} else {
		var fallback string
		for _, summary := range catalog.Snapshots {
			snapshot, snapshotErr := repo.LoadSnapshot(ctx, taskName, summary.ID, password)
			if snapshotErr != nil {
				continue
			}
			snapshots[snapshot.ID] = snapshot
			if fallback == "" {
				fallback = snapshot.ID
			}
			if selected == "" && snapshot.Complete {
				selected = snapshot.ID
			}
		}
		if selected == "" {
			selected = fallback
		}
	}
	if selected == "" {
		return OpenResult{}, errors.New("任务中没有可恢复的索引")
	}
	s.mutex.Lock()
	s.taskDir = absolute
	s.task = task
	s.catalog = catalog
	s.snapshots = snapshots
	s.selectedID = selected
	s.repository = repo
	s.salvaged = false
	s.mutex.Unlock()
	return s.result(), nil
}

func (s *Session) result() OpenResult {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	summaries := []model.SnapshotSummary{}
	for _, snapshot := range s.snapshots {
		summaries = append(summaries, repository.SnapshotSummary(snapshot))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].CreatedAt.After(summaries[j].CreatedAt) })
	return OpenResult{Task: s.task.Public(), Snapshots: summaries, Selected: s.selectedID, Salvaged: s.salvaged}
}

func (s *Session) Select(snapshotID string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if _, exists := s.snapshots[snapshotID]; !exists {
		return os.ErrNotExist
	}
	s.selectedID = snapshotID
	return nil
}

func (s *Session) Files() ([]model.FileEntry, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	snapshot, exists := s.snapshots[s.selectedID]
	if !exists {
		return nil, errors.New("尚未打开任务")
	}
	return append([]model.FileEntry(nil), snapshot.Files...), nil
}

func (s *Session) Restore(ctx context.Context, selected []string, output string) (restore.Report, error) {
	s.mutex.RLock()
	task := s.task
	snapshot, exists := s.snapshots[s.selectedID]
	repo := s.repository
	s.mutex.RUnlock()
	if !exists || repo == nil {
		return restore.Report{}, errors.New("尚未打开任务")
	}
	engine := restore.Engine{Repository: repo}
	report, err := engine.Restore(ctx, task, snapshot, selected, output)
	if err == nil {
		_, _, _ = restore.WriteReport(output, report)
	}
	return report, err
}

func Salvage(ctx context.Context, taskDirectory, password string) (model.Task, model.Snapshot, error) {
	taskName := filepath.Base(taskDirectory)
	files := map[string]*model.FileEntry{}
	objects := []model.ObjectRecord{}
	var taskID, encodedSalt string
	var derivedKey []byte
	err := filepath.WalkDir(filepath.Join(taskDirectory, "objects"), func(objectPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".wcb") {
			return nil
		}
		file, err := os.Open(objectPath)
		if err != nil {
			return err
		}
		var objectReader *cryptox.ObjectReader
		if derivedKey == nil {
			objectReader, err = cryptox.OpenObject(file, password)
			if err == nil {
				taskID = objectReader.Header.TaskID
				encodedSalt = objectReader.Header.Salt
				salt, saltErr := cryptox.DecodeSalt(encodedSalt)
				if saltErr != nil {
					err = saltErr
				} else {
					derivedKey, err = cryptox.DeriveKey(password, salt, objectReader.Header.KDF)
				}
			}
		} else {
			objectReader, err = cryptox.OpenObjectWithKey(file, derivedKey)
		}
		if err != nil {
			_ = file.Close()
			return err
		}
		var metadata model.ObjectPayloadMetadata
		if err := json.Unmarshal(objectReader.Metadata, &metadata); err != nil {
			_ = file.Close()
			return err
		}
		info, err := entry.Info()
		if err != nil {
			_ = file.Close()
			return err
		}
		relative, _ := filepath.Rel(taskDirectory, objectPath)
		relative = filepath.ToSlash(relative)
		record := model.ObjectRecord{Path: relative, ID: metadata.ObjectID, GroupID: metadata.GroupID, Part: metadata.Part, TotalParts: metadata.TotalParts, Size: info.Size()}
		objects = append(objects, record)
		for _, payloadFile := range metadata.Files {
			item := files[payloadFile.FileID]
			if item == nil {
				item = &model.FileEntry{ID: payloadFile.FileID, RootAlias: payloadFile.RootAlias, RelativePath: payloadFile.RelativePath, Size: payloadFile.Size, Hash: payloadFile.Hash, Times: payloadFile.Times}
				files[payloadFile.FileID] = item
			}
			item.Blocks = append(item.Blocks, model.BlockRef{
				ObjectPath: relative, ObjectID: metadata.ObjectID, GroupID: metadata.GroupID,
				Part: metadata.Part, TotalParts: metadata.TotalParts,
				Offset: payloadFile.Offset, Length: payloadFile.Length, FileOffset: payloadFile.FileOffset,
				ObjectSize: info.Size(),
			})
		}
		return file.Close()
	})
	if err != nil {
		return model.Task{}, model.Snapshot{}, err
	}
	if len(files) == 0 || taskID == "" {
		return model.Task{}, model.Snapshot{}, errors.New("没有找到可自恢复的数据块")
	}
	entries := make([]model.FileEntry, 0, len(files))
	missing := []string{}
	for _, item := range files {
		sort.Slice(item.Blocks, func(i, j int) bool { return item.Blocks[i].FileOffset < item.Blocks[j].FileOffset })
		var length int64
		for _, block := range item.Blocks {
			length += block.Length
		}
		if length != item.Size {
			item.MissingReason = "自恢复扫描发现分块不完整"
			missing = append(missing, filepath.ToSlash(filepath.Join(item.RootAlias, item.RelativePath)))
		}
		entries = append(entries, *item)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RootAlias+"/"+entries[i].RelativePath < entries[j].RootAlias+"/"+entries[j].RelativePath
	})
	now := time.Now().UTC()
	task := model.Task{ID: taskID, Name: taskName, Mode: model.TaskModeArchive, Password: password, Salt: encodedSalt, Status: model.TaskReadOnly, CreatedAt: now, UpdatedAt: now}
	snapshot := model.Snapshot{ID: "salvaged", TaskID: taskID, CreatedAt: now, Complete: len(missing) == 0, Files: entries, Objects: objects, MissingFiles: missing}
	return task, snapshot, nil
}

func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := file.WriteTo(hasher); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
