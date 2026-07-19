//go:build windows

package fsmeta

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func SetTimes(path string, created, modified time.Time) error {
	if err := os.Chtimes(path, modified, modified); err != nil {
		return err
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	createdFiletime := windows.NsecToFiletime(created.UnixNano())
	modifiedFiletime := windows.NsecToFiletime(modified.UnixNano())
	return windows.SetFileTime(handle, &createdFiletime, nil, &modifiedFiletime)
}
