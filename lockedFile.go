package main

import (
	"os"
	"syscall"
)

// lockedFile is a wrapper around `os.File` that adds `.Open()` and overrides `.Close()` methods so that the
// underlying file is exclusively locked (via `flock(..., LOCK_EX)`) when accessed.
type lockedFile struct {
	*os.File
}

// Open opens the file as in `os.Open` and locks the file in exclusive mode via `flock(..., LOCK_EX)`,
// possibly blocking.
//
// If an error occurs in either step, it is reported and the internals are cleaned up (i.e. no need for the caller to
// call `.Close()`), otherwise the object must be `.Close()`d to release the lock and the file descriptor.
func (lf *lockedFile) Open(path string) error {
	var err error
	lf.File, err = os.Open(path)
	if err != nil {
		log.Errorf("Failed to Open: %v", err)
		return internalError("failed to Open inside lockedFile", err)
	}
	err = syscall.Flock(int(lf.File.Fd()), syscall.LOCK_EX)
	if err != nil {
		log.Errorf("Failed to get exclusive lock on %s: %v", lf.File.Name(), err)
		lf.File.Close() // An error is going to be returned, so the caller won't call `.Close()`
		return internalError("failed to get exclusive Flock", err)
	}
	return nil
}

// Close releases the lock on the underlying file and closes the file using its original `.Close()`.
// If an error occurs when releasing the lock, it is logged and returned; the error from the original
// `.Close()` is ignored.
func (lf *lockedFile) Close() error {
	defer lf.File.Close()
	err := syscall.Flock(int(lf.File.Fd()), syscall.LOCK_UN)
	if err != nil {
		log.Criticalf("Failed to release lock on %s: %v", lf.File.Name(), err)
		return err
	}
	return nil
}
