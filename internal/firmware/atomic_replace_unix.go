//go:build !windows

package firmware

import (
	"os"
	"path/filepath"
	"syscall"
)

type directoryPublishLock struct {
	file *os.File
}

func acquirePublishLock(directory string) (*directoryPublishLock, error) {
	file, err := os.OpenFile(filepath.Join(directory, ".publish.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &directoryPublishLock{file: file}, nil
}

func (lock *directoryPublishLock) Close() error {
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
