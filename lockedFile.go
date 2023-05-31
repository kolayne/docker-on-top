package main

import (
	"os"
	"syscall"
)

type lockedFile struct {
	*os.File
}

func (lf *lockedFile) Open(path string) error {
	var err error
	lf.File, err = os.Open(path)
	if err != nil {
		log.Errorf("Failed to Open: %v", err)
		return internalError("failed to Open inside lockedFile", err)
	}
	err = syscall.Flock(int(lf.File.Fd()), syscall.LOCK_EX)
	if err != nil {
		// `syscall.Flock` operates on file descriptor, so the error won't contain the file path, thus adding it manually
		log.Errorf("Failed to get exclusive lock on %s: %v", lf.File.Name(), err)
		return internalError("failed to get exclusive Flock", err)
	}
	return nil
}

func (lf *lockedFile) Close() error {
	defer lf.File.Close()
	err := syscall.Flock(int(lf.File.Fd()), syscall.LOCK_UN)
	if err != nil {
		log.Criticalf("Failed to release lock on %s: %v", lf.File.Name(), err)
		return err
	}
	return nil
}
