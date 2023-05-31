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

// The base directory of docker-on-top, where all the overlays' internals will be saved
const dotBaseDir string = "/var/lib/docker-on-top/"

func init() {
	err := os.MkdirAll(dotBaseDir, os.ModePerm)
	if err != nil {
		log.Fatalf("Failed to MkdirAll dotBaseDir: %v", dotBaseDir, err)
	}
}

func internalError(help string, err error) error {
	return fmt.Errorf("docker-on-top internal error: %s: %w", help, err)
}

type VolumeData struct {
	Name        string
	BaseDirPath string
	Volatile    bool
	MountsCount int // The number of containers using the volume at the moment
}

/*
Here is how the internal volume management works.

Each existing volume has a corresponding "main directory" named the same as the volume name located at the `dotBaseDir`
directory. For example, if a volume with name FooBar is created, the main directory
corresponding to it is /var/lib/docker-on-top/FooBar/ (as long as `dotBaseDir` is /var/lib/docker-on-top/).
A volume's main directory is created when the volume is created and removed when the volume is removed.

Inside a volume's main directory there are the following files/directories:
	- upper/  - the upperdir of an overlay mount. Exists always. For volatile mounts, recreated from scratch on every
		mount (unless the volume is already mounted to another container). On unmount no special action occurs.
	- workdir/  - the workdir of an overlay mount. Exists only when the volume is mounted.
	- mountpoint/  - the directory where the overlay is to be mounted to. Exists only when the volume is mounted.
*/

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	volumes map[string]VolumeData
}

func (d *Driver) mountpoint(volumeName string) string {
	return dotBaseDir + volumeName + "/mountpoint"
}

func (d *Driver) upperdir(volumeName string) string {
	return dotBaseDir + volumeName + "/upper"
}

func (d *Driver) workdir(volumeName string) string {
	return dotBaseDir + volumeName + "/workdir"
}

// This regex is based on the error message from docker daemon when requested to create a volume with invalid name
var volNameFormat = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")

func (d *Driver) Create(request *volume.CreateRequest) error {
	log.Debugf("Request Create: Name=%s Options=%s", request.Name, request.Options)

	if _, ok := d.volumes[request.Name]; ok {
		log.Debug("Volume already exists. New volume not created")
		return errors.New("volume already exists")
	}

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

	if err := os.Mkdir(dotBaseDir+request.Name, os.ModePerm); err != nil {
		if os.IsExist(err) {
			log.Debug("Volume's main directory already exists. New volume not created")
			return errors.New("volume already exists")
		}
		// Some other error - not expected
		log.Errorf("Failed to Mkdir main directory: %v. Volume not created", err)
		return internalError("failed to Mkdir volume main directory", err)
	}

	// Try to create upperdir. On failure, revert the creation of the volume main directory
	if err := os.Mkdir(d.upperdir(request.Name), os.ModePerm); err != nil {
		log.Errorf("Failed to Mkdir %s: %v. Removing the volume", request.Name, err)
		cleanupErr := os.RemoveAll(dotBaseDir + request.Name)
		if cleanupErr != nil {
			log.Errorf("Failed to RemoveAll main directory: %v", dotBaseDir+request.Name, cleanupErr)
		}
		return internalError("failed to Mkdir upperdir", err)
	}

	d.volumes[request.Name] = VolumeData{Name: request.Name, BaseDirPath: baseDir, Volatile: volatile, MountsCount: 0}
	return nil
}

func (d *Driver) List() (*volume.ListResponse, error) {
	log.Debug("Request List")

	var response volume.ListResponse
	for volumeName := range d.volumes {
		response.Volumes = append(response.Volumes, &volume.Volume{Name: volumeName})
	}
	log.Debugf("Volumes listed: %s", response.Volumes)
	return &response, nil
}

func (d *Driver) Get(request *volume.GetRequest) (*volume.GetResponse, error) {
	log.Debugf("Request Get: Name=%s", request.Name)

	if _, ok := d.volumes[request.Name]; ok {
		log.Debug("Found volume. Listing it (just its name)")
		return &volume.GetResponse{Volume: &volume.Volume{Name: request.Name}}, nil
	} else {
		log.Debug("The requested volume does not exist")
		return nil, errors.New("no such volume")
	}
}

func (d *Driver) Remove(request *volume.RemoveRequest) error {
	log.Debugf("Request Remove: Name=%s. It will succeed regardless of the presence of the volume", request.Name)
	if _, ok := d.volumes[request.Name]; ok {
		// Expecting the volume to have been unmounted by this moment. If it isn't, the error will be reported
		err := os.RemoveAll(dotBaseDir + request.Name)
		if err != nil {
			log.Errorf("Failed to RemoveAll main directory: %v", err)
			return internalError("failed to RemoveAll volume main directory", err)
		}
	}
	delete(d.volumes, request.Name)
	return nil
}

func (d *Driver) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Request Path: Name=%s", request.Name)
	return &volume.PathResponse{Mountpoint: d.mountpoint(request.Name)}, nil
}

func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)

	thisVol := d.volumes[request.Name] // Assuming the volume exists, as the docker daemon was supposed to check that

	mountpoint := d.mountpoint(request.Name)
	response := volume.MountResponse{Mountpoint: mountpoint}

	if thisVol.MountsCount > 0 {
		// Already used in some other container
		log.Debugf("Volume %s is already mounted for some other container. Indicating success without remounting",
			request.Name)
		thisVol.MountsCount++
		d.volumes[request.Name] = thisVol
		return &response, nil
	}

	lowerdir := d.volumes[request.Name].BaseDirPath
	upperdir := d.upperdir(request.Name)
	workdir := d.workdir(request.Name)

	err1 := os.Mkdir(mountpoint, os.ModePerm)
	err2 := os.Mkdir(workdir, os.ModePerm)
	err := errors.Join(err1, err2)
	if err != nil {
		log.Errorf("Failed to Mkdir mountpoint, workdir: %v, %v", err1, err2)

		// Attempt to cleanup
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
		err := os.RemoveAll(upperdir)
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
	// TODO: escape commas in directory names
	data := "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir

	err = syscall.Mount("docker-on-top_"+request.Name, mountpoint, fstype, 0, data)
	if err != nil {
		log.Errorf("Failed to mount %s: %v", request.Name, err)
		return nil, err
	}

	log.Debugf("Mounted volume %s at %s", request.Name, mountpoint)
	thisVol.MountsCount++
	d.volumes[request.Name] = thisVol
	return &response, nil
}

func (d *Driver) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)
	thisVol := d.volumes[request.Name] // Assuming the volume exists, as the docker daemon was supposed to check that
	if thisVol.MountsCount > 1 {
		// Volume is still mounted in some other container(s)
		log.Debugf("Volume %s is still mounted in some other container. Indicating success without unmounting",
			request.Name)
		thisVol.MountsCount--
		d.volumes[request.Name] = thisVol
		return nil
	}

	err := syscall.Unmount(d.mountpoint(request.Name), 0)
	if err != nil {
		log.Errorf("Failed to unmount %s: %v", request.Name, err)
		return err
	}

	err1 := os.Remove(d.mountpoint(request.Name))
	err2 := os.RemoveAll(d.workdir(request.Name))
	err = errors.Join(err1, err2)
	if err != nil {
		log.Errorf("Cleanup of %s failed. Errors for mountpoint, workdir: %v, %v",
			request.Name, err1, err2)
		// Don't return yet. The error will be returned later
	}

	// Regardless of whether cleanup succeeded, decrement the counter, as it tracks the number of active _mounts_,
	// and the unmount system call was successful.
	thisVol.MountsCount--
	d.volumes[request.Name] = thisVol

	// Report an error during cleanup, if any, but most likely will just be `nil`
	return err
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
