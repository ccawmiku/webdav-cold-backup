package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/fsmeta"
	"github.com/ccawmiku/webdav-cold-backup/internal/model"
)

var excludedDirectories = map[string]struct{}{
	"#recycle":     {},
	"@recycle":     {},
	"$recycle.bin": {},
	".trash":       {},
	"@eadir":       {},
}

type File struct {
	RootAlias    string
	RelativePath string
	AbsolutePath string
	Size         int64
	Modified     time.Time
	Created      time.Time
}

type Result struct {
	Files           []File
	UnstableFiles   []File
	IgnoredSymlinks int
	IgnoredSystem   int
	IgnoredUnstable int
}

func ValidateSources(sources []model.SourceRoot) error {
	if len(sources) == 0 {
		return errors.New("at least one source root is required")
	}
	aliases := make(map[string]struct{}, len(sources))
	cleaned := make([]string, len(sources))
	for index, source := range sources {
		if strings.TrimSpace(source.Alias) == "" {
			return fmt.Errorf("source %d has no restore alias", index+1)
		}
		aliasKey := strings.ToLower(source.Alias)
		if _, exists := aliases[aliasKey]; exists {
			return fmt.Errorf("duplicate source alias %q", source.Alias)
		}
		aliases[aliasKey] = struct{}{}
		absolute, err := filepath.Abs(source.Path)
		if err != nil {
			return fmt.Errorf("resolve source %q: %w", source.Path, err)
		}
		cleaned[index] = filepath.Clean(absolute)
		info, err := os.Stat(cleaned[index])
		if err != nil {
			return fmt.Errorf("source %q is unavailable: %w", source.Path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("source %q is not a directory", source.Path)
		}
	}
	for left := range cleaned {
		for right := left + 1; right < len(cleaned); right++ {
			if isNested(cleaned[left], cleaned[right]) || isNested(cleaned[right], cleaned[left]) {
				return fmt.Errorf("nested source roots are not allowed: %q and %q", sources[left].Path, sources[right].Path)
			}
		}
	}
	return nil
}

func Scan(ctx context.Context, sources []model.SourceRoot, stablePeriod time.Duration, now time.Time) (Result, error) {
	if err := ValidateSources(sources); err != nil {
		return Result{}, err
	}
	result := Result{}
	for _, source := range sources {
		root, _ := filepath.Abs(source.Path)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("read %q: %w", path, walkErr)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if path == root {
				return nil
			}
			nameKey := strings.ToLower(entry.Name())
			if entry.IsDir() {
				if _, excluded := excludedDirectories[nameKey]; excluded {
					result.IgnoredSystem++
					return fs.SkipDir
				}
				if entry.Type()&os.ModeSymlink != 0 {
					result.IgnoredSymlinks++
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				result.IgnoredSymlinks++
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("read metadata for %q: %w", path, err)
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if stablePeriod > 0 && info.ModTime().After(now.Add(-stablePeriod)) {
				result.IgnoredUnstable++
				relative, relErr := filepath.Rel(root, path)
				if relErr == nil {
					result.UnstableFiles = append(result.UnstableFiles, File{
						RootAlias:    source.Alias,
						RelativePath: filepath.ToSlash(relative),
						AbsolutePath: path,
						Size:         info.Size(),
						Modified:     info.ModTime(),
						Created:      fsmeta.CreatedTime(path, info),
					})
				}
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return fmt.Errorf("resolve relative path for %q: %w", path, err)
			}
			result.Files = append(result.Files, File{
				RootAlias:    source.Alias,
				RelativePath: filepath.ToSlash(relative),
				AbsolutePath: path,
				Size:         info.Size(),
				Modified:     info.ModTime(),
				Created:      fsmeta.CreatedTime(path, info),
			})
			return nil
		})
		if err != nil {
			return Result{}, err
		}
	}
	sort.Slice(result.Files, func(left, right int) bool {
		if result.Files[left].RootAlias != result.Files[right].RootAlias {
			return result.Files[left].RootAlias < result.Files[right].RootAlias
		}
		return result.Files[left].RelativePath < result.Files[right].RelativePath
	})
	return result, nil
}

func isNested(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." {
		return relative == "."
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
