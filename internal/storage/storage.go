package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotFound = errors.New("storage object not found")

type Info struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	IsDir      bool      `json:"isDir"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Store interface {
	MkdirAll(ctx context.Context, path string) error
	Put(ctx context.Context, path string, source io.Reader, size int64) error
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Stat(ctx context.Context, path string) (Info, error)
	List(ctx context.Context, path string) ([]Info, error)
	Move(ctx context.Context, source, destination string) error
	Delete(ctx context.Context, path string) error
}
