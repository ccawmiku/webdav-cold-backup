package object

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/cryptox"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
	"github.com/google/uuid"
)

const EncryptionReserve int64 = 16 * 1024 * 1024

type SourceFile struct {
	ID           string
	AbsolutePath string
	RootAlias    string
	RelativePath string
	Size         int64
	Hash         string
	Times        model.FileTimes
}

type Slice struct {
	File       SourceFile
	FileOffset int64
	Length     int64
	DataOffset int64
}

type BuildSpec struct {
	TaskID        string
	EncodedSalt   string
	Key           []byte
	MaxObjectSize int64
	GroupID       string
	Part          int
	TotalParts    int
	Kind          string
	Slices        []Slice
	CacheDir      string
}

type BuiltObject struct {
	TempPath   string
	RemotePath string
	Record     model.ObjectRecord
	Metadata   model.ObjectPayloadMetadata
}

func MaxPayloadSize(maxObjectSize int64) (int64, error) {
	if maxObjectSize <= 2*EncryptionReserve {
		return 0, errors.New("object size is too small")
	}
	return maxObjectSize - EncryptionReserve, nil
}

func NewGroupID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func Build(ctx context.Context, spec BuildSpec) (BuiltObject, error) {
	if err := ctx.Err(); err != nil {
		return BuiltObject{}, err
	}
	if len(spec.Slices) == 0 {
		return BuiltObject{}, errors.New("object requires at least one file slice")
	}
	if spec.GroupID == "" {
		spec.GroupID = NewGroupID()
	}
	if spec.Part < 1 || spec.TotalParts < spec.Part {
		return BuiltObject{}, errors.New("invalid object part numbering")
	}
	if spec.Kind == "" {
		spec.Kind = "data"
	}
	if err := os.MkdirAll(spec.CacheDir, 0o700); err != nil {
		return BuiltObject{}, fmt.Errorf("create cache directory: %w", err)
	}

	objectID := strings.ReplaceAll(uuid.NewString(), "-", "")
	prefix := spec.GroupID[:2]
	remotePath := fmt.Sprintf("objects/%s/%s/part-%05d-%s.wcb", prefix, spec.GroupID, spec.Part, objectID)
	tempPath := filepath.Join(spec.CacheDir, objectID+".partial")

	metadata := model.ObjectPayloadMetadata{
		FormatVersion: model.FormatVersion,
		TaskID:        spec.TaskID,
		ObjectID:      objectID,
		GroupID:       spec.GroupID,
		Part:          spec.Part,
		TotalParts:    spec.TotalParts,
		Kind:          spec.Kind,
		Files:         make([]model.PayloadFileRecord, 0, len(spec.Slices)),
	}
	var totalPayload int64
	for _, slice := range spec.Slices {
		if slice.Length < 0 || slice.FileOffset < 0 || slice.FileOffset+slice.Length > slice.File.Size {
			return BuiltObject{}, fmt.Errorf("invalid slice for %s", slice.File.RelativePath)
		}
		metadata.Files = append(metadata.Files, model.PayloadFileRecord{
			FileID: slice.File.ID, RootAlias: slice.File.RootAlias,
			RelativePath: slice.File.RelativePath, Size: slice.File.Size,
			Hash: slice.File.Hash, Times: slice.File.Times,
			Offset: totalPayload, Length: slice.Length, FileOffset: slice.FileOffset,
		})
		totalPayload += slice.Length
	}
	maxPayload, err := MaxPayloadSize(spec.MaxObjectSize)
	if err != nil {
		return BuiltObject{}, err
	}
	if totalPayload > maxPayload {
		return BuiltObject{}, fmt.Errorf("payload %d exceeds safe object payload %d", totalPayload, maxPayload)
	}

	payload := &sliceSequenceReader{slices: spec.Slices}
	defer payload.Close()
	header, err := cryptox.NewHeader(spec.TaskID, objectID, spec.Kind, spec.EncodedSalt)
	if err != nil {
		return BuiltObject{}, err
	}
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return BuiltObject{}, fmt.Errorf("create temporary object: %w", err)
	}
	result, encryptErr := cryptox.EncryptObject(file, spec.Key, header, metadata, payload)
	closeErr := file.Close()
	if encryptErr != nil {
		_ = os.Remove(tempPath)
		return BuiltObject{}, fmt.Errorf("encrypt object: %w", encryptErr)
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return BuiltObject{}, fmt.Errorf("close temporary object: %w", closeErr)
	}
	if result.Size > spec.MaxObjectSize {
		_ = os.Remove(tempPath)
		return BuiltObject{}, fmt.Errorf("encrypted object %d exceeds configured maximum %d", result.Size, spec.MaxObjectSize)
	}
	return BuiltObject{
		TempPath:   tempPath,
		RemotePath: remotePath,
		Record: model.ObjectRecord{
			Path: remotePath, ID: objectID, GroupID: spec.GroupID,
			Part: spec.Part, TotalParts: spec.TotalParts,
			Size: result.Size, Hash: result.SHA256,
		},
		Metadata: metadata,
	}, nil
}

type sliceSequenceReader struct {
	slices  []Slice
	index   int
	current *os.File
	section *io.SectionReader
}

func (r *sliceSequenceReader) Read(buffer []byte) (int, error) {
	for {
		if r.section == nil {
			if r.index >= len(r.slices) {
				return 0, io.EOF
			}
			slice := r.slices[r.index]
			file, err := os.Open(slice.File.AbsolutePath)
			if err != nil {
				return 0, fmt.Errorf("open source file %s: %w", slice.File.RelativePath, err)
			}
			r.current = file
			r.section = io.NewSectionReader(file, slice.FileOffset, slice.Length)
		}
		n, err := r.section.Read(buffer)
		if errors.Is(err, io.EOF) {
			_ = r.current.Close()
			r.current = nil
			r.section = nil
			r.index++
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *sliceSequenceReader) Close() error {
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

func HashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	buffer := make([]byte, 4*1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hasher.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func NewSourceFile(alias, relativePath, absolutePath string, size int64, hash string, modified, created time.Time) SourceFile {
	return SourceFile{
		ID:           strings.ReplaceAll(uuid.NewString(), "-", ""),
		AbsolutePath: absolutePath,
		RootAlias:    alias,
		RelativePath: filepath.ToSlash(relativePath),
		Size:         size,
		Hash:         hash,
		Times:        model.FileTimes{Modified: modified, Created: created},
	}
}
