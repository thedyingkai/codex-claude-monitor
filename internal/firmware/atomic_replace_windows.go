//go:build windows

package firmware

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type directoryPublishLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquirePublishLock(directory string) (*directoryPublishLock, error) {
	file, err := os.OpenFile(filepath.Join(directory, ".publish.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := &directoryPublishLock{file: file}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &lock.overlapped); err != nil {
		_ = file.Close()
		return nil, err
	}
	return lock, nil
}

func (lock *directoryPublishLock) Close() error {
	unlockErr := windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &lock.overlapped)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func atomicReplace(source, destination string) error {
	return windows.Rename(source, destination)
}

// The production publisher runs on Linux, where directory metadata is
// explicitly fsynced. Windows does not expose an equivalent directory fsync
// through os.File; MoveFileEx still provides atomic replacement semantics.
func syncDirectory(string) error { return nil }
