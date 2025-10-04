package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/docker/go-plugins-helpers/volume"
)

// This regex is based on the error message from docker daemon when requested to create a volume with invalid name
var volNameFormat = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")

func (d *DockerOnTop) Create(request *volume.CreateRequest) error {
	log.Debugf("Request Create: Name=%s Options=%s", request.Name, request.Options)

	if !volNameFormat.MatchString(request.Name) {
		log.Debug("Volume name doesn't comply to the regex. Volume not created")
		if strings.ContainsRune(request.Name, '/') {
			// Handle this case separately for a more specific error message
			return errors.New("volume name cannot contain slashes (for specifying host path use " +
				"`-o base=/path/to/base/directory`)")
		}
		return errors.New("volume name contains illegal characters: " +
			"it should comply to \"[a-zA-Z0-9][a-zA-Z0-9_.-]*\"")
	}

	allowedOptions := map[string]bool{"base": true, "volatile": true} // Values are meaningless, only keys matter
	for opt := range request.Options {
		if _, ok := allowedOptions[opt]; !ok {
			log.Debugf("Unknown option %s. Volume not created", opt)
			return errors.New("Invalid option " + opt)
		}
	}

	baseDir, ok := request.Options["base"]
	if !ok {
		log.Debug("No `base` option was provided. Volume not created")
		return errors.New("`base` option must be provided and set to an absolute path to the base directory on host")
	}

	if len(baseDir) < 1 || baseDir[0] != '/' {
		log.Debug("`base` is not an absolute path. Volume not created")
		return errors.New("`base` must be an absolute path")
	} else if strings.ContainsRune(baseDir, ',') || strings.ContainsRune(baseDir, ':') {
		log.Debug("`base` contains a comma or a colon. Volume not created")
		return errors.New("directories with commas and/or colons in the path are not supported")
	} else {
		// Check that the base directory exists

		f, err := os.Open(baseDir)
		if os.IsNotExist(err) {
			// The base directory does not exist. Note that it doesn't make sense to implicitly create it (as docker
			// does by default with bind mounts), as the point of docker-on-top is to let containers work _on top_ of
			// an existing host directory, so implicitly making an empty one would be pointless.
			log.Debugf("The base directory %s does not exist. Volume not created", baseDir)
			return errors.New("the base directory does not exist")
		} else if err != nil {
			log.Errorf("Failed to open base directory: %v. Volume not created", err)
			return fmt.Errorf("the specified base directory is inaccessible: %w", err)
		} else {
			_ = f.Close()
		}
	}

	var volatile bool
	volatileS, ok := request.Options["volatile"]
	if !ok {
		volatileS = "false"
	}
	volatileS = strings.ToLower(volatileS)
	if volatileS == "no" || volatileS == "false" {
		volatile = false
	} else if volatileS == "yes" || volatileS == "true" {
		volatile = true
	} else {
		log.Debug("Option `volatile` has an invalid value. Volume not created")
		return errors.New("option `volatile` must be either 'true', 'false', 'yes', or 'no'")
	}

	if err := d.volumeTreeCreate(request.Name); err != nil {
		if os.IsExist(err) {
			log.Debug("Volume's main directory already exists. New volume not created")
			return errors.New("volume already exists")
		} else {
			// The error is already logged and wrapped in `internalError` by `d.volumeTreeCreate`
			return err
		}
	}

	if err := d.writeVolumeInfo(request.Name, VolumeInfo{BaseDirPath: baseDir, Volatile: volatile}); err != nil {
		log.Errorf("Failed to write metadata for volume %s: %v. Aborting volume creation (attempting "+
			"to destroy the volume's tree)", request.Name, err)
		_ = d.volumeTreeDestroy(request.Name) // The errors are logged, if any
		return internalError("failed to store metadata for the volume", err)
	}

	return nil
}

func (d *DockerOnTop) List() (*volume.ListResponse, error) {
	log.Debug("Request List")

	var response volume.ListResponse
	entries, err := os.ReadDir(d.dotRootDir)
	if err != nil {
		log.Errorf("Failed to list contents of the dot root directory: %v", err)
		return nil, internalError("failed to list contents of the dot root directory", err)
	}
	for _, volMainDir := range entries {
		response.Volumes = append(response.Volumes, &volume.Volume{Name: volMainDir.Name()})
	}
	return &response, nil
}

func (d *DockerOnTop) Get(request *volume.GetRequest) (*volume.GetResponse, error) {
	log.Debugf("Request Get: Name=%s", request.Name)

	// Note: the implementation does not  ensure that `d.dotRootDir + request.Name` is a directory.
	// I don't think it's worth checking, though, as under the normal plugin operation (with no interference from
	// third parties) only directories are created in `d.dotRootDir`

	dir, err := os.Open(d.dotRootDir + request.Name)
	if err == nil {
		_ = dir.Close()
		log.Debug("Found volume. Listing it (just its name)")
		return &volume.GetResponse{Volume: &volume.Volume{Name: request.Name}}, nil
	} else if os.IsNotExist(err) {
		log.Debug("The requested volume does not exist")
		return nil, errors.New("no such volume")
	} else {
		log.Errorf("Failed to open the volume's main directory: %v", err)
		return nil, internalError("failed to open the volume's main directory", err)
	}
}

func (d *DockerOnTop) Remove(request *volume.RemoveRequest) error {
	log.Debugf("Request Remove: Name=%s. It will succeed regardless of the presence of the volume", request.Name)

	// If dockerd sent us this request, it means no containers are using the volume.
	// Under normal operation, it means that mountpoint must not exist already.
	//
	// If it stil exists, though, we will try to recover now. If recovery fails, we report
	// failure but the volume remains in a consistent state (nothing is removed).

	mountpoint := d.mountpointdir(request.Name)

	// Try to remove it in case it's not mounted
	err := os.Remove(mountpoint)
	if errors.Is(err, syscall.EBUSY) {
		// Try to unmount
		err = syscall.Unmount(mountpoint, syscall.MNT_FORCE|syscall.MNT_DETACH)
		if err == nil {
			log.Warningf("Unmounted (force+detach) volume %s, which is being removed", request.Name)
		} else if os.IsNotExist(err) {
			log.Warningf("The mountpoint %s existed and has literally just disappeared, what is going on?",
				mountpoint)
		} else {
			log.Errorf("Volume %s to be removed is still mounted and cannot be unmounted: %v", err)
			return internalError("failed to unmount volume mountpoint when removing", err)
		}
	} else if os.IsNotExist(err) {
		// That's the default case: mountpoint does not exist
	} else if err != nil {
		log.Errorf("Volume %s to be removed does not seem mounted but still cannot be removed: %v",
			request.Name, err)
		return internalError("failed to remove the mountpoint directory of a seemingly unmounted volume",
			err)
	}

	err = os.RemoveAll(d.dotRootDir + request.Name)
	if err != nil {
		// This potentially leaves volume directory in an inconsistent state :(
		log.Errorf("Failed to RemoveAll main directory for volume %s: %v", request.Name, err)
		return internalError("failed to RemoveAll volume main directory", err)
	}
	return nil
}

func (d *DockerOnTop) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Request Path: Name=%s", request.Name)
	return &volume.PathResponse{Mountpoint: d.mountpointdir(request.Name)}, nil
}

func (d *DockerOnTop) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)

	_, err := d.getVolumeInfo(request.Name)
	if os.IsNotExist(err) {
		log.Debugf("Couldn't get volume info: %v", err)
		return nil, errors.New("no such volume")
	} else if err != nil {
		log.Errorf("Failed to retrieve metadata for volume %s: %v", request.Name, err)
		return nil, internalError("failed to retrieve the volume's metadata", err)
	}

	// Synchronization. Take an exclusive lock on the activemounts/ dir of the volume to ensure that no parallel
	// mounts/unmounts interfere. Note that it is crucial that the lock is held not only during the checks on other
	// containers using the volume, but until a complete mount/unmount is performed: if, instead, we unlocked after
	// finding that we are the first mount request (thus responsible to mount) but before actually mounting, another
	// thread will see that the volume is already in use and assume it is mounted (while it isn't yet),
	// which is a race condition.
	var activemountsdir lockedFile
	err = activemountsdir.Open(d.activemountsdir(request.Name))
	if err != nil {
		// The error is already logged and wrapped in `internalError` in lockedFile.go
		return nil, err
	}
	defer activemountsdir.Close() // There is nothing I could do about the error (logging is performed inside `Close()` anyway)

	err = d.activateVolume(request.Name, request.ID, activemountsdir)
	if err == nil {
		mountpoint := d.mountpointdir(request.Name)
		response := volume.MountResponse{Mountpoint: mountpoint}
		return &response, nil
	} else {
		return nil, err
	}
}

func (d *DockerOnTop) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)

	// Synchronization. Taking an exclusive lock on activemounts/ of the volume so that parallel mounts/unmounts
	// don't interfere.
	// For more details, read the comment at the beginning of `DockerOnTop.Mount`.
	var activemountsdir lockedFile
	err := activemountsdir.Open(d.activemountsdir(request.Name))
	if err != nil {
		// The error is already logged and wrapped in `internalError` in lockedFile.go
		return err
	}
	defer activemountsdir.Close() // There's nothing I can do about the error if it occurs

	err = d.deactivateVolume(request.Name, request.ID, activemountsdir)
	return err
}

func (d *DockerOnTop) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}

// =======================================================================================
// | CONCEPTUAL NOTE: an existing file at the active mounts directory is a guarantee of      |
// | another container using the volume; the absense of files at the active mounts directory |
// | is a guarantee that no container is using the volume.                                   |
// |                                                                                         |
// | The existence of a mountpoint directory or an overlay mounted on it is a side effect,   |
// | which does not guarantee anything, and shall not be relied upon.                        |
// =======================================================================================

// activateVolume activates a volume: checks if other containers are using it already,
// mounts it if needed, and handles all the internal state matters.
//
// The volume must exist, otherwise the function panics.
//
// Parameters:
//
//	volumeName: Name of the volume to be mounted
//	requestId: Unique ID of the mount request
//	activemountsdir: Folder where mounts are tracked (with an exclusive lock taken)
//
// Return:
//
//	err: An error, if encountered
func (d *DockerOnTop) activateVolume(volumeName string, requestId string, activemountsdir lockedFile) error {
	thisVol, err := d.getVolumeInfo(volumeName)
	if err != nil {
		panic(err)
	}

	_, err = activemountsdir.ReadDir(1) // Check if there are any files inside activemounts dir
	if errors.Is(err, io.EOF) {
		// No files => no other containers are using the volume. Need to mount the overlay

		lowerdir := thisVol.BaseDirPath
		upperdir := d.upperdir(volumeName)
		workdir := d.workdir(volumeName)
		mountpoint := d.mountpointdir(volumeName)

		err = d.volumeTreePreMount(volumeName, thisVol.Volatile)
		if err != nil {
			// The error is already logged and wrapped in `internalError` by `d.volumeTreePreMount`
			return err
		}

		options := "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir

		err = syscall.Mount("docker-on-top_"+volumeName, mountpoint, "overlay", 0, options)
		if os.IsNotExist(err) {
			log.Errorf("Failed to mount overlay for volume %s because something does not exist: %v",
				volumeName, err)
			return errors.New("failed to mount volume: something is missing (does the base directory exist?)")
		} else if err != nil {
			log.Errorf("Failed to mount overlay for volume %s: %v", volumeName, err)
			return internalError("failed to mount overlay", err)
		}

		log.Debugf("Mounted volume %s at %s", volumeName, mountpoint)
	} else if err == nil {
		log.Debugf("Volume %s is already mounted for some other container. Indicating success without remounting",
			volumeName)
	} else {
		log.Errorf("Failed to list the activemounts directory: %v", err)
		return internalError("failed to list activemounts/", err)
	}

	activemountFilePath := d.activemountsdir(volumeName) + requestId
	f, err := os.Create(activemountFilePath)
	if err == nil {
		// We don't care about the file's contents
		_ = f.Close()
	} else if os.IsExist(err) {
		// Super weird. I can't imagine why this would happen.
		log.Warningf("Active mount %s already exists (but it shouldn't...)", activemountFilePath)
	} else {
		// We have successfully mounted the overlay but failed to mark that we are using it.
		// If we use the volume now, we break the guarantee that we shall provide according
		// to the above note. Thus, refusing with an error.
		// We leave the overlay mounted as a harmless side effect.
		log.Errorf("While mounting volume %s, failed to create active mount file: %v", volumeName, err)
		return internalError("failed to create active mount file while mounting volume", err)
	}

	return nil
}

// deactivateVolume deactivates a volume: checks if other containers are still using it,
// unmounts it if needed and handles all the internal state matters.
//
// The volume must exist, otherwise the function panics.
//
// Parameters:
//
//	volumeName: Name of the volume to be mounted
//	requestId: Unique ID of the mount request
//	activemountsdir: Folder where mounts are tracked (with an exclusive lock taken)
func (d *DockerOnTop) deactivateVolume(volumeName string, requestId string, activemountsdir lockedFile) error {
	// In accordance with the conceptual note above, we must first remove the file from the active mounts dir,
	// and then attempt to unmount overlay. This ensures that if we crash mid-way, the volume state is consistent:
	// a mounted overlay is a harmless side effect, but an active mount file may only exist if the volume is in use.

	activemountFilePath := d.activemountsdir(volumeName) + requestId

	err := os.Remove(activemountFilePath)
	if os.IsNotExist(err) {
		log.Warningf("Failed to remove %s because it does not exist (but it should...)", activemountFilePath)
	} else if err != nil {
		log.Errorf("Failed to remove active mounts file %s. The volume %s is now stuck in the active state",
			activemountFilePath, volumeName)
		// The user most likely won't see this error message due to daemon not showing unmount errors to the
		// `docker run` clients :((
		return internalError("failed to remove the active mount file; the volume is now stuck in the active state", err)
	}

	_, err = activemountsdir.ReadDir(1) // Check if there is any container using the volume (after us)
	if errors.Is(err, io.EOF) {
		err = syscall.Unmount(d.mountpointdir(volumeName), 0)
		if err != nil {
			log.Errorf("Failed to unmount %s: %v", d.mountpointdir(volumeName), err)
			return err
		}

		err = d.volumeTreePostUnmount(volumeName)
		return err
	} else if err == nil {
		log.Debugf("Volume %s is still mounted in another container. Indicating success without unmounting",
			volumeName)
		return nil
	} else {
		log.Errorf("Failed to list the activemounts directory: %v", err)
		return internalError("failed to list activemounts/ ", err)
	}
}
