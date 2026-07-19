//go:build windows

package fsmeta

import (
	"os"
	"syscall"
	"time"
)

func CreatedTime(path string, info os.FileInfo) time.Time {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, data.CreationTime.Nanoseconds())
	}
	return info.ModTime()
}
