package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(configDir string) (*Store, error) {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	databasePath := filepath.Join(configDir, "webdav-cold-backup.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			json BLOB NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			task_id TEXT NOT NULL,
			id TEXT NOT NULL,
			json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(task_id, id),
			FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			task_id TEXT NOT NULL,
			id TEXT NOT NULL,
			json BLOB NOT NULL,
			started_at TEXT NOT NULL,
			PRIMARY KEY(task_id, id),
			FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task_started ON runs(task_id, started_at DESC)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			json BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS remote_presets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			json BLOB NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
	}
	defaultJSON, _ := json.Marshal(model.DefaultSettings())
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings(id, json) VALUES(1, ?)`, defaultJSON); err != nil {
		return fmt.Errorf("initialize settings: %w", err)
	}
	return nil
}

func (s *Store) SaveRemotePreset(ctx context.Context, preset model.RemotePreset) error {
	encoded, err := json.Marshal(preset)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_presets(id, name, json, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, json=excluded.json, updated_at=excluded.updated_at`,
		preset.ID, preset.Name, encoded, preset.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) RemotePreset(ctx context.Context, id string) (model.RemotePreset, error) {
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, `SELECT json FROM remote_presets WHERE id=?`, id).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.RemotePreset{}, os.ErrNotExist
		}
		return model.RemotePreset{}, err
	}
	var preset model.RemotePreset
	if err := json.Unmarshal(encoded, &preset); err != nil {
		return model.RemotePreset{}, err
	}
	return preset, nil
}

func (s *Store) RemotePresets(ctx context.Context) ([]model.RemotePreset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json FROM remote_presets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.RemotePreset{}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var preset model.RemotePreset
		if err := json.Unmarshal(encoded, &preset); err != nil {
			return nil, err
		}
		items = append(items, preset)
	}
	return items, rows.Err()
}

func (s *Store) DeleteRemotePreset(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM remote_presets WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *Store) SaveTask(ctx context.Context, task model.Task) error {
	encoded, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tasks(id, name, json, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, json=excluded.json, updated_at=excluded.updated_at`,
		task.ID, task.Name, encoded, task.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Task(ctx context.Context, id string) (model.Task, error) {
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, `SELECT json FROM tasks WHERE id=?`, id).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Task{}, os.ErrNotExist
		}
		return model.Task{}, err
	}
	var task model.Task
	if err := json.Unmarshal(encoded, &task); err != nil {
		return model.Task{}, err
	}
	return task, nil
}

func (s *Store) Tasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json FROM tasks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []model.Task{}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var task model.Task
		if err := json.Unmarshal(encoded, &task); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) DeleteTask(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *Store) SaveSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO snapshots(task_id, id, json, created_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(task_id, id) DO UPDATE SET json=excluded.json, created_at=excluded.created_at`,
		snapshot.TaskID, snapshot.ID, encoded, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Snapshot(ctx context.Context, taskID, id string) (model.Snapshot, error) {
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, `SELECT json FROM snapshots WHERE task_id=? AND id=?`, taskID, id).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Snapshot{}, os.ErrNotExist
		}
		return model.Snapshot{}, err
	}
	var snapshot model.Snapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return model.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) Snapshots(ctx context.Context, taskID string) ([]model.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json FROM snapshots WHERE task_id=? ORDER BY created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.Snapshot{}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var snapshot model.Snapshot
		if err := json.Unmarshal(encoded, &snapshot); err != nil {
			return nil, err
		}
		items = append(items, snapshot)
	}
	return items, rows.Err()
}

func (s *Store) DeleteSnapshot(ctx context.Context, taskID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE task_id=? AND id=?`, taskID, id)
	return err
}

func (s *Store) SaveRun(ctx context.Context, run model.RunRecord) error {
	encoded, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(task_id, id, json, started_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(task_id, id) DO UPDATE SET json=excluded.json, started_at=excluded.started_at`,
		run.TaskID, run.ID, encoded, run.StartedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Runs(ctx context.Context, taskID string, limit int) ([]model.RunRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT json FROM runs WHERE task_id=? ORDER BY started_at DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.RunRecord{}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var run model.RunRecord
		if err := json.Unmarshal(encoded, &run); err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

func (s *Store) SaveSettings(ctx context.Context, settings model.GlobalSettings) error {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE settings SET json=? WHERE id=1`, encoded)
	return err
}

func (s *Store) Settings(ctx context.Context) (model.GlobalSettings, error) {
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, `SELECT json FROM settings WHERE id=1`).Scan(&encoded); err != nil {
		return model.GlobalSettings{}, err
	}
	var settings model.GlobalSettings
	if err := json.Unmarshal(encoded, &settings); err != nil {
		return model.GlobalSettings{}, err
	}
	return settings, nil
}
