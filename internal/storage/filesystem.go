package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileStore struct {
	root string
}

func NewFileStore(root string) (*FileStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve storage root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open storage root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("storage root is not a directory")
	}
	return &FileStore{root: filepath.Clean(absolute)}, nil
}

func (s *FileStore) resolve(path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(path, "/")))
	if clean == "." {
		return s.root, nil
	}
	resolved := filepath.Join(s.root, clean)
	relative, err := filepath.Rel(s.root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage path escapes root")
	}
	return resolved, nil
}

func (s *FileStore) MkdirAll(_ context.Context, path string) error {
	resolved, err := s.resolve(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(resolved, 0o700)
}

func (s *FileStore) Put(ctx context.Context, path string, source io.Reader, _ int64) error {
	resolved, err := s.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(resolved, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, &contextReader{context: ctx, reader: source})
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (s *FileStore) Open(_ context.Context, path string) (io.ReadCloser, error) {
	resolved, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(resolved)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return file, err
}

func (s *FileStore) Stat(_ context.Context, path string) (Info, error) {
	resolved, err := s.resolve(path)
	if err != nil {
		return Info{}, err
	}
	info, err := os.Stat(resolved)
	if os.IsNotExist(err) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, err
	}
	return Info{Path: filepath.ToSlash(path), Name: info.Name(), Size: info.Size(), IsDir: info.IsDir(), ModifiedAt: info.ModTime()}, nil
}

func (s *FileStore) List(_ context.Context, path string) ([]Info, error) {
	resolved, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	items := make([]Info, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		itemPath := filepath.ToSlash(filepath.Join(path, entry.Name()))
		items = append(items, Info{Path: itemPath, Name: entry.Name(), Size: info.Size(), IsDir: info.IsDir(), ModifiedAt: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *FileStore) Move(_ context.Context, source, destination string) error {
	from, err := s.resolve(source)
	if err != nil {
		return err
	}
	to, err := s.resolve(destination)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	if _, err := os.Stat(to); err == nil {
		if err := os.Remove(to); err != nil {
			return err
		}
		return os.Rename(from, to)
	}
	return os.Rename(from, to)
}

func (s *FileStore) Delete(_ context.Context, path string) error {
	resolved, err := s.resolve(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(resolved)
	}
	return os.Remove(resolved)
}

type contextReader struct {
	context context.Context
	reader  io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
