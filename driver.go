package main

import (
	"errors"
	"fmt"
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

	// Expecting the volume to have been unmounted by this moment. If it isn't, the error will be reported
	err := os.RemoveAll(d.dotRootDir + request.Name)
	if err != nil {
		log.Errorf("Failed to RemoveAll main directory: %v", err)
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

	thisVol, err := d.getVolumeInfo(request.Name)
	if os.IsNotExist(err) {
		log.Debugf("Couldn't get volume info: %v", err)
		return nil, errors.New("no such volume")
	} else if err != nil {
		log.Errorf("Failed to retrieve metadata for volume %s: %v", request.Name, err)
		return nil, internalError("failed to retrieve the volume's metadata", err)
	}

	mountpoint := d.mountpointdir(request.Name)
	response := volume.MountResponse{Mountpoint: mountpoint}

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

	doMountFs, err := d.activateVolume(request.Name, request.ID, activemountsdir)
	if err != nil {
		log.Errorf("Error while activating the filesystem mount: %v", err)
		return nil, internalError("failed to activate an active mount:", err)
	} else if doMountFs {
		lowerdir := thisVol.BaseDirPath
		upperdir := d.upperdir(request.Name)
		workdir := d.workdir(request.Name)

		err = d.volumeTreePreMount(request.Name, thisVol.Volatile)
		if err != nil {
			// The error is already logged and wrapped in `internalError` by `d.volumeTreePreMount`
			return nil, err
		}

		options := "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir

		err = syscall.Mount("docker-on-top_"+request.Name, mountpoint, "overlay", 0, options)
		if err != nil {
			// The filesystem could not be mounted so undo the activateVolume call so it does not appear as if
			// we are using a volume that we couln't mount. We can ignore the doUnmountFs because we know the volume
			// is not mounted.
			_, deactivateErr := d.deactivateVolume(request.Name, request.ID, activemountsdir)
			if deactivateErr != nil {
				log.Errorf("Additional error while deactivating the filesystem mount: %v", err)
				// Do not return the error since we are dealing with a more important one
			}

			if os.IsNotExist(err) {
				log.Errorf("Failed to mount overlay for volume %s because something does not exist: %v",
					request.Name, err)
				return nil, errors.New("failed to mount volume: something is missing (does the base directory " +
					"exist?)")
			} else {
				log.Errorf("Failed to mount overlay for volume %s: %v", request.Name, err)
				return nil, internalError("failed to mount overlay", err)
			}
		}
		log.Debugf("Mounted volume %s at %s", request.Name, mountpoint)
	} else {
		log.Debugf("Volume %s already mounted at %s", request.Name, mountpoint)
	}

	return &response, nil
}

func (d *DockerOnTop) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)

	// Assuming the volume exists: the docker daemon won't let remove a volume that is still mounted

	// Synchronization. Taking an exclusive lock on activemounts/ of the volume so that parallel mounts/unmounts
	// don't interfere.
	// For more details, read the comment in the beginning of `DockerOnTop.Mount`.
	var activemountsdir lockedFile
	err := activemountsdir.Open(d.activemountsdir(request.Name))
	if err != nil {
		// The error is already logged and wrapped in `internalError` in lockedFile.go
		return err
	}
	defer activemountsdir.Close() // There's nothing I could do about the error if it occurs

	doUnmountFs, err := d.deactivateVolume(request.Name, request.ID, activemountsdir)
	if err != nil {
		log.Errorf("Error while activating the filesystem mount: %v", err)
		return internalError("failed to deactivate the active mount:", err)
	} else if doUnmountFs {
		err = syscall.Unmount(d.mountpointdir(request.Name), 0)
		if err != nil {
			log.Errorf("Failed to unmount %s: %v", d.mountpointdir(request.Name), err)
			return err
		}
		err = d.volumeTreePostUnmount(request.Name)

		log.Debugf("Unmounted volume %s", request.Name)
	} else {
		log.Debugf("Volume %s is still used in another container. Indicating success without unmounting", request.Name)
	}

	// Report an error during cleanup, if any
	return err
}

func (d *DockerOnTop) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
