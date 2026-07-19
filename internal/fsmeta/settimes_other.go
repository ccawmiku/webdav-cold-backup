//go:build !windows

package fsmeta

import (
	"os"
	"time"
)

func SetTimes(path string, _ time.Time, modified time.Time) error {
	return os.Chtimes(path, modified, modified)
}
