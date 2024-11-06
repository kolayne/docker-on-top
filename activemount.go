package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type activeMount struct {
	UsageCount int
}

// activateVolume checks if the volume that has been requested to be mounted (as in docker volume mounting)
// actually requires to be mounted (as an overlay fs mount). For that purpose check if other containers
// have already mounted the volume (by reading in `activemountsdir`). It is also possible that the volume
// has already been been mounted by the same container (when doing a `docker cp` while the container is running),
// in that case the file named with the `requestId` will contain the number of times the container has
// been requested the volume to be mounted. That number will be increased each time `activateVolume` is
// called and decreased on `deactivateVolume`.
//
// Parameters:
//
//	requestName: Name of the volume to be mounted
//	requestID: Unique ID for the volume-container pair requesting the mount
//	activemountsdir: Folder where Docker-On-Top mounts are tracked.
//
// Return:
//
//	doMountFs: `true` if the caller should mount the filesystem, `false` otherwise.
//		If an error is returned, `doMountFs` is always `false`.
//	err: An error, if encountered, `nil` otherwise.
func (d *DockerOnTop) activateVolume(requestName string, requestId string, activemountsdir lockedFile) (bool, error) {
	var doMountFs bool

	_, readDirErr := activemountsdir.ReadDir(1) // Check if there are any files inside activemounts dir
	if readDirErr == nil {
		// There is something no need to mount the filesystem again
		doMountFs = false
	} else if errors.Is(readDirErr, io.EOF) {
		// The directory is empty, mount the filesystem
		doMountFs = true
	} else {
		return false, fmt.Errorf("failed to list activemounts/ : %w", readDirErr)
	}

	var activeMountInfo activeMount
	activemountFilePath := d.activemountsdir(requestName) + requestId

	payload, readErr := os.ReadFile(activemountFilePath)

	if readErr == nil {
		// The file can exist from a previous mount when doing a docker cp on an already mounted container, no need to mount the filesystem again
		unmarshalErr := json.Unmarshal(payload, &activeMountInfo)
		if unmarshalErr != nil {
			return false, fmt.Errorf("active mount file %s contents are invalid: %w", activemountFilePath, unmarshalErr)
		}
	} else if os.IsNotExist(readErr) {
		// Default case, we need to create a new active mount, the filesystem needs to be mounted
		activeMountInfo = activeMount{UsageCount: 0}
	} else {
		return false, fmt.Errorf("active mount file %s exists but cannot be read: %w", activemountFilePath, readErr)
	}

	activeMountInfo.UsageCount++

	// Convert activeMountInfo to JSON to store it in a file. We can safely ignore Marshal errors, since the
	// activeMount structure is simple enought not to contain "strange" floats, unsupported datatypes or cycles,
	// which are the error causes for json.Marshal
	payload, _ = json.Marshal(activeMountInfo)
	writeErr := os.WriteFile(activemountFilePath, payload, 0o644)
	if writeErr != nil {
		return false, fmt.Errorf("active mount file %s cannot be written %w", activemountFilePath, writeErr)
	}

	return doMountFs, nil
}

// deactivateVolume checks if the volume that has been requested to be unmounted (as in docker volume unmounting)
// actually requires to be unmounted (as an overlay fs unmount). It will check the number of times the container
// has been requested to mount the volume in the file named `requestId` and decrease the number, when the number
// reaches zero it will delete the `requestId` file since this container no longer mounts the volume. It will
// also check if other containers are mounting this volume by checking for other files in the active mounts folder.
//
// Parameters:
//
//	requestName: Name of the volume to be unmounted
//	requestID: Unique ID for the volume-container pair requesting the mount
//	activemountsdir: Folder where Docker-On-Top mounts are tracked.
//
// Return:
//
//	doUnmountFs: `true` if there are no other usages of this volume and the filesystem should be unmounted
//		by the caller. If an error is returned, `doMountFs` is always `false`.
//	err: An error, if encountered, `nil` otherwise.
func (d *DockerOnTop) deactivateVolume(requestName string, requestId string, activemountsdir lockedFile) (bool, error) {

	dirEntries, readDirErr := activemountsdir.ReadDir(2) // Check if there is any _other_ container using the volume
	if errors.Is(readDirErr, io.EOF) {
		// If directory is empty, unmount overlay and clean up
		log.Warning("there are no active mount files and one was expected. the filesystem will be unmounted")
		return true, nil
	} else if readDirErr != nil {
		return false, fmt.Errorf("failed to list activemounts/ %w", readDirErr)
	}

	otherVolumesPresent := len(dirEntries) > 1 || dirEntries[0].Name() != requestId

	var activeMountInfo activeMount
	activemountFilePath := d.activemountsdir(requestName) + requestId

	payload, readErr := os.ReadFile(activemountFilePath)

	if readErr == nil {
		unmarshalErr := json.Unmarshal(payload, &activeMountInfo)
		if unmarshalErr != nil {
			return false, fmt.Errorf("active mount file %s contents are invalid %w, the filesystem won't be unmounted", activemountFilePath, unmarshalErr)
		}
	} else if os.IsNotExist(readErr) {
		return !otherVolumesPresent, fmt.Errorf("the active mount file %s was expected but is not there %w, the filesystem won't be unmounted", activemountFilePath, readErr)
	} else {
		return false, fmt.Errorf("the active mount file %s could not be opened %w, the filesystem won't be unmounted", activemountFilePath, readErr)
	}

	activeMountInfo.UsageCount--

	if activeMountInfo.UsageCount == 0 {
		err := os.Remove(activemountFilePath)
		if err != nil {
			return false, fmt.Errorf("the active mount file %s could not be deleted %w, the filesystem won't be unmounted", activemountFilePath, err)
		}
		return !otherVolumesPresent, nil
	} else {
		// Convert activeMountInfo to JSON to store it in a file. We can safely ignore Marshal errors, since the
		// activeMount structure is simple enought not to contain "strage" floats, unsupported datatypes or cycles
		// which are the error causes for json.Marshal
		payload, _ := json.Marshal(activeMountInfo)
		writeErr := os.WriteFile(activemountFilePath, payload, 0o644)
		if writeErr != nil {
			return false, fmt.Errorf("the active mount file %s could not be updated %w, the filesystem won't be unmounted", activemountFilePath, writeErr)
		}
		return false, nil
	}
}
