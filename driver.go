package main

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/docker/go-plugins-helpers/volume"
)

// The base directory of docker-on-top, where all the overlays' internals will be saved
const dotBaseDir string = "/var/lib/docker-on-top/"

type VolumeData struct {
	Name        string
	BaseDirPath string
	Volatile    bool
	MountsCount int // The number of containers using the volume at the moment
}

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	volumes map[string]VolumeData
}

func (d *Driver) getMountpoint(volumeName string) string {
	return dotBaseDir + d.volumes[volumeName].Name + "/mountpoint"
}

func (d *Driver) getUpperdir(volumeName string) string {
	return dotBaseDir + d.volumes[volumeName].Name + "/upper"
}

func (d *Driver) getWorkdir(volumeName string) string {
	return dotBaseDir + d.volumes[volumeName].Name + "/workdir"
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
		// Expecting the volume to have been unmounted by this moment
		err := os.RemoveAll(dotBaseDir + request.Name)
		if err != nil {
			return err
		}
	}
	delete(d.volumes, request.Name)
	return nil
}

func (d *Driver) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Request Path: Name=%s", request.Name)
	return &volume.PathResponse{Mountpoint: d.getMountpoint(request.Name)}, nil
}

func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)

	thisVol := d.volumes[request.Name] // Assuming the volume exists, as the docker daemon was supposed to check that

	mountpoint := d.getMountpoint(request.Name)
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
	upperdir := d.getUpperdir(request.Name)
	workdir := d.getWorkdir(request.Name)

	for _, dir := range []string{lowerdir, upperdir, workdir, mountpoint} {
		// TODO: create workdir with 000 permission
		err := os.MkdirAll(dir, os.ModePerm)
		if err != nil {
			log.Errorf("Failed to create %s: %v", dir, err)
			return nil, err
		}
	}

	fstype := "overlay"
	// TODO: escape commas in directory names
	data := "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir

	err := syscall.Mount("docker-on-top_"+request.Name, mountpoint, fstype, 0, data)
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

	err := syscall.Unmount(d.getMountpoint(request.Name), 0)
	if err != nil {
		log.Errorf("Failed to unmount %s: %v", request.Name, err)
		return err
	}

	// For volatile volume, discard changes on last unmount
	if thisVol.Volatile {
		err1 := os.RemoveAll(d.getUpperdir(request.Name))
		err2 := os.RemoveAll(d.getWorkdir(request.Name))
		err = errors.Join(err1, err2)
		if err != nil {
			log.Errorf("Failed to remove upperdir,workdir for the volatile volume %s: %v", request.Name, err)
		}
	}

	// Regardless of error with volatile cleanup, decrement the counter, as it tracks the number of active _mounts_,
	// and the unmount system call was successful.
	thisVol.MountsCount--
	d.volumes[request.Name] = thisVol

	// Report an error during volatility cleanup, if any, but most likely will just be `nil`
	return err
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
