package main

import (
	"errors"
	"os"
)

/*
Here is how the internal volume management works.

Each existing volume has a corresponding "main directory" named the same as the volume name and located inside the
"dot root directory". For example, if the dot root directory is /var/lib/docker-on-top/ and a volume with name FooBar is
created, that volume's main directory is /var/lib/docker-on-top/FooBar/
The volume's main directory is created when a volume is created and removed (together with *all* of its contents)
when the volume is removed.

Inside a volume's main directory there are the following files/directories:
    - metadata.json  - stores the volume's metadata, which comprises the options it was created with.
	- activemounts/  - stores information about containers currently using the volume. Each file in it corresponds to
		a container, the name of the file container+mount ID (received from the docker daemon).
		On mount/unmount operations, an exclusive lock (via `flock`) is taken on this directory _for the whole process_
		of mounting/unmounting.
	- upper/  - the upperdir of an overlay mount. Exists always. For volatile mounts, recreated from scratch on every
		mount (unless the volume is already mounted to another container). On unmount no special action occurs.
	- workdir/  - the workdir of an overlay mount. Exists only when the volume is mounted.
	- mountpoint/  - the directory where the overlay is to be mounted to. Exists only when the volume is mounted.
*/

func (d *DockerOnTop) activemountsdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/activemounts/"
}

func (d *DockerOnTop) upperdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/upper/"
}

func (d *DockerOnTop) workdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/workdir/"
}

func (d *DockerOnTop) mountpointdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/mountpoint/"
}

// volumeTreeCreate creates a directory tree for the specified volume (but not metadata.json).
//
// If errors occur, they are logged and the returned error is wrapped with `internalError`, except when volume already
// exists. In that case, nothing is logged and `os.ErrExist` is returned (without being wrapped).
func (d *DockerOnTop) volumeTreeCreate(volumeName string) error {
	if err := os.Mkdir(d.dotRootDir+volumeName, os.ModePerm); err != nil {
		if os.IsExist(err) {
			return err
		} else {
			log.Errorf("Failed to Mkdir main directory: %v", err)
			return internalError("failed to Mkdir volume main directory", err)
		}
	}

	// Try to create internal directories. On failure, revert the creation of the volume main directory
	for _, dir := range []string{d.upperdir(volumeName), d.activemountsdir(volumeName)} {
		if err := os.Mkdir(dir, os.ModePerm); err != nil {
			log.Errorf("Failed to Mkdir internal directory: %v. Aborting volume creation (attempting "+
				"to destroy the volume's tree)", volumeName, err)
			_ = d.volumeTreeDestroy(volumeName) // The errors are logged, if any
			return internalError("failed to Mkdir internal directories", err)
		}
	}

	return nil
}

// volumeTreeDestroy destroys the directory tree for the specified volume, **recursively removing everything** inside
// the volume's main directory, including any files/directories not created by the plugin, if any.
//
// In errors occur, they are logged and the returned error is wrapped with `internalError`.
// Note that if the volume doesn't exist, the function call is considered successful (`nil` is returned).
func (d *DockerOnTop) volumeTreeDestroy(volumeName string) error {
	err := os.RemoveAll(d.dotRootDir + volumeName)
	if err != nil {
		log.Errorf("Failed to RemoveAll main directory: %v", err)
		return internalError("failed to RemoveAll volume main directory", err)
	}
	return nil
}

// volumeTreePreMount creates the directories in the volume's directory tree that should only exist when the volume
// is mounted.
//
// If either the mountpoint or the workdir directories exist, it is logged as a warning but not considered an error.
//
// If errors occur, they are logged and the returned error is wrapped with `internalError`.
func (d *DockerOnTop) volumeTreePreMount(volumeName string, discardUpper bool) error {
	mountpoint := d.mountpointdir(volumeName)
	workdir := d.workdir(volumeName)

	err1 := os.Mkdir(mountpoint, os.ModePerm)
	if os.IsExist(err1) {
		log.Warningf("Mountpoint of %s already exists. It usually means that the overlay is already mounted "+
			"but the plugin failed to detect it...", volumeName)
	}
	err2 := os.Mkdir(workdir, os.ModePerm)
	if os.IsExist(err2) {
		log.Warningf("Workdir of %s already exists. It usually means that the overlay is already mounted but "+
			"the plugin failed to detect it...", volumeName)
	}
	err := errors.Join(err1, err2)
	if (err1 != nil && !os.IsExist(err1)) || (err2 != nil && !os.IsExist(err2)) {
		log.Errorf("Failed to Mkdir mountpoint, workdir: %v, %v", err1, err2)

		// Attempt to clean up. Only remove the directories that we created moments ago

		if err1 == nil {
			cleanupErr := os.Remove(mountpoint)
			if cleanupErr != nil {
				log.Errorf("Failed to cleanup mountpoint: %v", cleanupErr)
			}
		}
		if err2 == nil {
			cleanupErr := os.Remove(workdir)
			if cleanupErr != nil {
				log.Errorf("Failed to cleanup workdir: %v", cleanupErr)
			}
		}

		return internalError("failed to prepare internal directories", err)
	}

	// For volatile volume, discard previous changes
	if discardUpper {
		upperdir := d.upperdir(volumeName)

		err = os.RemoveAll(upperdir)
		if err != nil {
			log.Errorf("Failed to RemoveAll upperdir (for volatile): %v", err)
			return internalError("failed to discard previous changes", err)
		}
		err = os.Mkdir(upperdir, os.ModePerm)
		if err != nil {
			log.Errorf("Failed to Mkdir upperdir (for volatile): %v", err)
			return internalError("failed to create upperdir after discarding changes", err)
		}
	}

	return nil
}

// volumeTreePostUnmount removes the directories in the volume's directory tree that should only exist when the volume
// is mounted.
//
// It removes the mountpoint directory (non-recursively: must be empty) and the workdir directory (recursively: all of
// its contents is deleted). No action is taken regarding upperdir, regardless of the volume's volatility.
//
// Removal of both directories is attempted regardless of errors with the other directory. Errors, if any, are logged,
// combined with `errors.Join` and returned (wrapped with `internalError`).
//
// Note: for technical reasons, the absense of the workdir directory is not considered an error.
func (d *DockerOnTop) volumeTreePostUnmount(volumeName string) error {
	err1 := os.Remove(d.mountpointdir(volumeName))
	err2 := os.RemoveAll(d.workdir(volumeName))
	err := errors.Join(err1, err2)
	if err != nil {
		log.Errorf("Cleanup of %s failed. Errors for mountpoint, workdir: %v, %v", volumeName, err1, err2)
		return internalError("failed to cleanup on unmount", err)
	}
	return nil
}
