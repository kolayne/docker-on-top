package main

import (
	"errors"
	"github.com/docker/go-plugins-helpers/volume"
	"os"
	"syscall"
)

// The base directory of docker-on-top, where all the overlays' internals will be saved
const dotBaseDir string = "/var/lib/docker-on-top/"

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	volumesCreated  map[string]volume.Volume
	overlaysCreated map[string]string
}

// Use https://docs.docker.com/engine/extend/plugins_volume/ as a reference for your implementation

func (d *Driver) Create(request *volume.CreateRequest) error {
	log.Debugf("Request Create: Name=%s Options=%s", request.Name, request.Options)
	if _, ok := d.volumesCreated[request.Name]; ok {
		log.Debug("Volume already exists. Refusing to create one")
		return errors.New("volume already exists")
	} else {
		d.volumesCreated[request.Name] = volume.Volume{Name: request.Name}
		d.overlaysCreated[request.Name] = dotBaseDir + request.Name
		return nil
	}
}

func (d *Driver) List() (*volume.ListResponse, error) {
	log.Debug("Request List")

	var response volume.ListResponse
	for volumeName := range d.volumesCreated {
		response.Volumes = append(response.Volumes, &volume.Volume{Name: volumeName})
	}
	log.Debugf("Volumes listed: %s", response.Volumes)
	return &response, nil
}

func (d *Driver) Get(request *volume.GetRequest) (*volume.GetResponse, error) {
	log.Debugf("Request Get: Name=%s", request.Name)

	if _, ok := d.volumesCreated[request.Name]; ok {
		log.Debug("Found volume. Listing it (just its name)")
		return &volume.GetResponse{Volume: &volume.Volume{Name: request.Name}}, nil
	} else {
		log.Debug("The requested volume does not exist")
		return nil, errors.New("no such volume")
	}
}

func (d *Driver) Remove(request *volume.RemoveRequest) error {
	log.Debugf("Request Remove: Name=%s. It will succeed regardless of the presence of the volume", request.Name)
	if overlayPath, ok := d.overlaysCreated[request.Name]; ok {
		// Expecting the volume to have been unmounted by this moment
		err := os.RemoveAll(overlayPath)
		if err != nil {
			return err
		}
	}
	delete(d.volumesCreated, request.Name)
	delete(d.overlaysCreated, request.Name)
	return nil
}

func (d *Driver) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Request Path: Name=%s", request.Name)
	return &volume.PathResponse{Mountpoint: d.volumesCreated[request.Name].Mountpoint}, nil
}

func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)
	lowerdir := dotBaseDir + request.Name + "/lower"
	upperdir := dotBaseDir + request.Name + "/upper"
	workdir := dotBaseDir + request.Name + "/workdir"
	mountpoint := dotBaseDir + request.Name + "/mountpoint"

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

	d.volumesCreated[request.Name] = volume.Volume{Name: request.Name, Mountpoint: mountpoint}
	log.Debugf("Mounted volume %s at %s", request.Name, mountpoint)
	response := volume.MountResponse{Mountpoint: mountpoint}
	return &response, nil
}

func (d *Driver) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)
	err := syscall.Unmount(d.overlaysCreated[request.Name]+"/mountpoint", 0)
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
