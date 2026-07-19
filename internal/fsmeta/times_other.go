//go:build !windows && !linux

package fsmeta

import (
	"os"
	"time"
)

func CreatedTime(_ string, info os.FileInfo) time.Time {
	return info.ModTime()
}
