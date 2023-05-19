package main

import (
	"errors"
	"github.com/docker/go-plugins-helpers/volume"
	"os"
	"strings"
	"syscall"
)

// The base directory of docker-on-top, where all the overlays' internals will be saved
const dotBaseDir string = "/var/lib/docker-on-top/"

type VolumeData struct {
	Name        string
	BaseDirPath string
}

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	volumes map[string]VolumeData
}

func (d *Driver) getMountpoint(volumeName string) string {
	return dotBaseDir + d.volumes[volumeName].Name + "/mountpoint"
}

func (d *Driver) Create(request *volume.CreateRequest) error {
	log.Debugf("Request Create: Name=%s Options=%s", request.Name, request.Options)
	if _, ok := d.volumes[request.Name]; ok {
		log.Debug("Volume already exists. Refusing to create one")
		return errors.New("volume already exists")
	} else if baseDir, ok := request.Options["base"]; ok {
		if len(baseDir) < 1 {
			log.Debug("`base` is empty. Volume not created")
			return errors.New("`base` option must not be empty")
		} else if baseDir[0] != '/' {
			log.Debug("`base` is not an absolute path. Volume not created")
			return errors.New("`base` must be an absolute path")
		} else if strings.ContainsRune(baseDir, ',') {
			log.Debug("`base` contains a comma. Volume not created")
			return errors.New("directories with a comma in the path are currently unsupported")
		} else {
			d.volumes[request.Name] = VolumeData{Name: request.Name, BaseDirPath: baseDir}
			return nil
		}
	} else {
		log.Debug("No `base` option was provided. Volume not created")
		return errors.New("`base` option must be provided and set to an absolute path to the base directory on host")
	}
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
	lowerdir := d.volumes[request.Name].BaseDirPath
	upperdir := dotBaseDir + request.Name + "/upper"
	workdir := dotBaseDir + request.Name + "/workdir"
	mountpoint := d.getMountpoint(request.Name)

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

	err := syscall.Mount("docker-on-top_"+request.ID, mountpoint, fstype, 0, data)
	if err != nil {
		log.Errorf("Failed to mount %s: %v", request.Name, err)
		return nil, err
	}

	log.Debugf("Mounted volume %s at %s", request.Name, mountpoint)
	response := volume.MountResponse{Mountpoint: mountpoint}
	return &response, nil
}

func (d *Driver) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)
	err := syscall.Unmount(d.getMountpoint(request.Name), 0)
	if err != nil {
		log.Errorf("Failed to unmount %s: %v", request.Name, err)
		return err
	}
	return nil
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
