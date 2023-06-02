package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/docker/go-plugins-helpers/volume"
)

func internalError(help string, err error) error {
	return fmt.Errorf("docker-on-top internal error: %s: %w", help, err)
}

type VolumeData struct {
	BaseDirPath string
	Volatile    bool
}

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

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	// dotRootDir is the base directory of docker-on-top, where all the internal information is stored.
	// Must contain a trailing slash.
	dotRootDir string
}

func NewDriver(dotRootDir string) (*Driver, error) {
	if len(dotRootDir) == 0 {
		return nil, errors.New("`dotRootDir` cannot be empty")
	}

	if dotRootDir[len(dotRootDir)-1] != '/' {
		dotRootDir += "/"
	}

	err := os.MkdirAll(dotRootDir, os.ModePerm)
	if err != nil {
		return nil, err
	}

	return &Driver{dotRootDir: dotRootDir}, nil
}

func MustNewDriver(baseDir string) *Driver {
	driver, err := NewDriver(baseDir)
	if err != nil {
		panic(fmt.Errorf("failed to MkdirAll %s: %v", baseDir, err))
	}
	return driver
}

func (d *Driver) activemountsdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/activemounts/"
}

func (d *Driver) upperdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/upper/"
}

func (d *Driver) workdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/workdir/"
}

func (d *Driver) mountpointdir(volumeName string) string {
	return d.dotRootDir + volumeName + "/mountpoint/"
}

func (d *Driver) getVolume(volumeName string) (VolumeData, error) {
	var vol VolumeData

	payload, err := os.ReadFile(d.dotRootDir + volumeName + "/metadata.json")
	if err == nil {
		err = json.Unmarshal(payload, &vol)
	}

	return vol, err
}

func (d *Driver) writeVolume(volumeName string, volume VolumeData) error {
	payload, err := json.Marshal(volume)

	if err == nil {
		err = os.WriteFile(d.dotRootDir+volumeName+"/metadata.json", payload, 0o666)
	}

	return err
}

// This regex is based on the error message from docker daemon when requested to create a volume with invalid name
var volNameFormat = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")

func (d *Driver) Create(request *volume.CreateRequest) error {
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

	if err := os.Mkdir(d.dotRootDir+request.Name, os.ModePerm); err != nil {
		if os.IsExist(err) {
			log.Debug("Volume's main directory already exists. New volume not created")
			return errors.New("volume already exists")
		}
		// Some other error - not expected
		log.Errorf("Failed to Mkdir main directory: %v. Volume not created", err)
		return internalError("failed to Mkdir volume main directory", err)
	}

	// Try to create upperdir. On failure, revert the creation of the volume main directory
	for _, dir := range []string{d.upperdir(request.Name), d.activemountsdir(request.Name)} {
		if err := os.Mkdir(dir, os.ModePerm); err != nil {
			log.Errorf("Failed to Mkdir internal directory: %v. Removing the volume", request.Name, err)
			cleanupErr := os.RemoveAll(d.dotRootDir + request.Name)
			if cleanupErr != nil {
				log.Errorf("Failed to RemoveAll main directory: %v", d.dotRootDir+request.Name, cleanupErr)
			}
			return internalError("failed to Mkdir internal directories", err)
		}
	}

	if err := d.writeVolume(request.Name, VolumeData{BaseDirPath: baseDir, Volatile: volatile}); err != nil {
		log.Errorf("Failed to write metadata for volume %s: %v", request.Name, err)
		// TODO: duplication here
		cleanupErr := os.RemoveAll(d.dotRootDir + request.Name)
		if cleanupErr != nil {
			log.Errorf("Failed to RemoveAll main directory: %v", d.dotRootDir+request.Name, cleanupErr)
		}
		return internalError("failed to store metadata for the volume", err)
	}

	return nil
}

func (d *Driver) List() (*volume.ListResponse, error) {
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
	log.Debugf("Volumes listed: %v", response.Volumes)
	return &response, nil
}

func (d *Driver) Get(request *volume.GetRequest) (*volume.GetResponse, error) {
	log.Debugf("Request Get: Name=%s", request.Name)

	// Note: the implementation does not  ensure that `d.dotRootDir + request.Name` is a file or a directory.
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

func (d *Driver) Remove(request *volume.RemoveRequest) error {
	log.Debugf("Request Remove: Name=%s. It will succeed regardless of the presence of the volume", request.Name)

	// Expecting the volume to have been unmounted by this moment. If it isn't, the error will be reported
	err := os.RemoveAll(d.dotRootDir + request.Name)
	if err != nil {
		log.Errorf("Failed to RemoveAll main directory: %v", err)
		return internalError("failed to RemoveAll volume main directory", err)
	}
	return nil
}

func (d *Driver) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Request Path: Name=%s", request.Name)
	return &volume.PathResponse{Mountpoint: d.mountpointdir(request.Name)}, nil
}

func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)

	// Assuming the volume exists, as the docker daemon was supposed to check that
	thisVol, err := d.getVolume(request.Name)
	if err != nil {
		log.Errorf("Failed to retrieve metadata for volume %s: %v", request.Name, err)
		return nil, internalError("failed to retrieve the volume's metadata", err)
	}

	mountpoint := d.mountpointdir(request.Name)
	response := volume.MountResponse{Mountpoint: mountpoint}

	// Synchronization. Take an exclusive lock on the activemounts/ dir to ensure that no parallel mounts/unmounts
	// interfere. Note that it is crucial that the lock is hold not only during the checks on other containers
	// using the volume, but until a complete mount/unmount is performed: if instead we unlocked after finding that
	// we are the first mount request (thus responsible to mount) but before actually mounting, another thread will
	// see that the volume is already in use and assume it is mounted (while it isn't yet), which is a race condition.
	var activemountsdir lockedFile
	err = activemountsdir.Open(d.activemountsdir(request.Name))
	if err != nil {
		// The error is already logged by lockedFile.go
		return nil, err
	}
	defer activemountsdir.Close() // There is nothing I could do about the error (logging is performed inside `Close()` anyway)

	_, readDirErr := activemountsdir.ReadDir(1) // Check if there are any files inside activemounts dir
	if errors.Is(readDirErr, io.EOF) {
		// No files => no other containers are using the volume. Need to actually mount

		lowerdir := thisVol.BaseDirPath
		upperdir := d.upperdir(request.Name)
		workdir := d.workdir(request.Name)

		err1 := os.Mkdir(mountpoint, os.ModePerm)
		if os.IsExist(err1) {
			log.Warningf("Mountpoint of %s already exists. It usually means that the overlay is already mounted "+
				"but the plugin failed to detect it. Trying to proceed anyway", request.Name)
		}
		err2 := os.Mkdir(workdir, os.ModePerm)
		if os.IsExist(err2) {
			log.Warningf("Workdir of %s already exists. It usually means that the overlay is already mounted but "+
				"the plugin failed to detect it. Trying to proceed anyway", request.Name)
		}
		err = errors.Join(err1, err2)
		if (err1 != nil && !os.IsExist(err1)) || (err2 != nil && !os.IsExist(err2)) {
			log.Errorf("Failed to Mkdir mountpoint, workdir: %v, %v", err1, err2)

			// Attempt to clean up
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

			return nil, internalError("failed to prepare internal directories", err)
		}

		// For volatile volume, discard previous changes
		if thisVol.Volatile {
			err = os.RemoveAll(upperdir)
			if err != nil {
				log.Errorf("Failed to RemoveAll upperdir (for volatile): %v", err)
				return nil, internalError("failed to discard previous changes", err)
			}
			err = os.Mkdir(upperdir, os.ModePerm)
			if err != nil {
				log.Errorf("Failed to Mkdir upperdir (for volatile): %v", err)
				return nil, internalError("failed to recreate upperdir", err)
			}
		}

		fstype := "overlay"
		data := "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir

		err = syscall.Mount("docker-on-top_"+request.Name, mountpoint, fstype, 0, data)
		if err != nil {
			log.Errorf("Failed to mount overlay for volume %s: %v", request.Name, err)
			return nil, fmt.Errorf("failed to mount overlay for the volume (does the base directory exist?): %w",
				err)
		}

		log.Debugf("Mounted volume %s at %s", request.Name, mountpoint)
	} else if err == nil {
		log.Debugf("Volume %s is already mounted for some other container. Indicating success without remounting",
			request.Name)
	} else {
		log.Errorf("Failed to list the activemounts directory: %v", err)
		return nil, internalError("failed to list activemounts/", err)
	}

	activemountFilePath := d.activemountsdir(request.Name) + request.ID
	f, err := os.Create(activemountFilePath)
	if err == nil {
		// We don't care about the file's contents
		_ = f.Close()
	} else {
		if os.IsExist(err) {
			// Super weird. I can't imagine why this would happen.
			log.Warningf("Active mount %s already exists (but it shouldn't...)", activemountFilePath)
		} else {
			// A really bad situation!
			// We successfully mounted (`syscall.Mount`) the volume but failed to put information about the container
			// using the volume. Via the plugin it is now impossible to mount this volume (as the overlay is already
			// mounted), to unmount this volume (as the unmount function won't find any containers using it),
			// or to remove it (the plugin will attempt to remove the volume's main directory but its mountpoint/ will
			// be busy).
			// Thus, a human interaction is required.
			log.Criticalf("Failed to create active mount file %s. This is fatal: this volume is now "+
				"impossible to either mount or unmount. Human interaction is required", activemountFilePath, err)
			return nil, fmt.Errorf("docker-on-top internal error: failed to create an active mount file: %w. "+
				"The volume is now locked (run `umount %s` to unlock). Human interaction is required. Please, report "+
				"this bug", err, mountpoint)
		}
	}

	return &response, nil
}

func (d *Driver) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)

	// Assuming the volume exists, as the docker daemon was supposed to check that

	// Synchronization. Taking an exclusive lock on activemounts/ so that parallel mounts/unmounts don't interfere.
	// For more details, read the comment in the beginning of `Driver.Mount`
	var activemountsdir lockedFile
	err := activemountsdir.Open(d.activemountsdir(request.Name))
	if err != nil {
		// The error is already logged by lockedFile.go
		return err
	}
	defer activemountsdir.Close() // There's nothing I could do about the error if it occurs

	dirEntries, readDirErr := activemountsdir.ReadDir(2) // Check if there is any _other_ container using the volume
	if len(dirEntries) == 1 || errors.Is(readDirErr, io.EOF) {
		// If just one entry or directory is empty, unmount overlay and clean up

		err = syscall.Unmount(d.mountpointdir(request.Name), 0)
		if err != nil {
			log.Errorf("Failed to unmount %s: %v", d.mountpointdir(request.Name), err)
			return err
		}

		err1 := os.Remove(d.mountpointdir(request.Name))
		err2 := os.RemoveAll(d.workdir(request.Name))
		err = errors.Join(err1, err2)
		if err != nil {
			log.Errorf("Cleanup of %s failed. Errors for mountpoint, workdir: %v, %v",
				request.Name, err1, err2)
			// Don't return yet. The error will be returned later
		}
		err = internalError("failed to cleanup on unmount", err)
	} else if readDirErr == nil {
		log.Debugf("Volume %s is still mounted in some other container. Indicating success without unmounting",
			request.Name)
	} else {
		log.Errorf("Failed to list the activemounts directory: %v", err)
		return internalError("failed to list activemounts/", err)
	}

	// Regardless of whether cleanup succeeded, should remove self from volume users so that on the next mount request
	// the plugin knows to mount the overlay again.
	activemountFilePath := d.activemountsdir(request.Name) + request.ID
	err2 := os.Remove(activemountFilePath)
	if os.IsNotExist(err2) {
		log.Warningf("Failed to remove %s because it does not exist (but it should...)", activemountFilePath)
	} else if err2 != nil {
		// Another pretty bad situation. Even though we are no longer using the volume, it is seemingly in use by us
		// because we failed to remove the file corresponding to this container.
		log.Criticalf("Failed to remove the active mount file: %v. The volume is now considered used by a container "+
			"that no longer exists", err)
		// The user most likely won't see this error message due to daemon not showing unmount errors to the
		// `docker run` clients :((
		return fmt.Errorf("docker-on-top internal error: failed to remove the active mount file: %w. The volume is "+
			"now considered used by a container that no longer exists. Human interaction is required: remove the file "+
			"manually to fix the problem", err)
	}

	// Report an error during cleanup, if any, but most likely will just be `nil`
	return err
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
