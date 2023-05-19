package main

import (
	"errors"
	"github.com/containers/buildah/pkg/overlay"
	"github.com/docker/go-plugins-helpers/volume"
	"os"
	"syscall"
)

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	// TODO: some fields will be here. For now it's just a stub implementation

	volumesCreated  map[string]volume.Volume // In this implementation the value does not mean anything, only the key
	overlaysCreated map[string]string
}

// Use https://docs.docker.com/engine/extend/plugins_volume/ as a reference for your implementation

func (d *Driver) Create(request *volume.CreateRequest) error {
	log.Debugf("Request Create: Name=%s Options=%s", request.Name, request.Options)
	if _, ok := d.volumesCreated[request.Name]; ok {
		log.Debug("Volume already exists. Refusing to create one")
		return errors.New("volume already exists")
	} else {
		log.Debugf("Pretending that the volume was successfully created")
		d.volumesCreated[request.Name] = volume.Volume{Name: request.Name}
		d.overlaysCreated[request.Name] = "/home/VolumeOverlays/" + request.Name
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
	overlay_path := d.overlaysCreated[request.Name]
	err := overlay.Unmount(overlay_path)
	if err != nil {
		return err
	}
	err = os.RemoveAll(overlay_path)
	if err != nil {
		return err
	}
	delete(d.volumesCreated, request.Name)
	return nil
}

func (d *Driver) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	return &volume.PathResponse{Mountpoint: d.volumesCreated[request.Name].Mountpoint}, nil
}


func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)
	mountPoint := "/mnt/overlay"
	errory := os.MkdirAll(mountPoint, os.ModePerm)
	if errory != nil {
		log.Fatalf("Failed to create mount point directory: %v", errory)
	}
	lowerdir := "/home/VolumeOverlays/" + request.Name + "/lower"
	errory = os.MkdirAll(lowerdir, os.ModePerm)
	if errory != nil {
		log.Fatalf("Failed to create mount point directory: %v", errory)
	}
	upperdir := "/home/VolumeOverlays/" + request.Name + "/upper"
	errory = os.MkdirAll(upperdir, os.ModePerm)
	if errory != nil {
		log.Fatalf("Failed to create mount point directory: %v", errory)
	}
	workdir := "/home/VolumeOverlays/" + request.Name + "/workdir"
	errory = os.MkdirAll(workdir, os.ModePerm)
	if errory != nil {
		log.Fatalf("Failed to create mount point directory: %v", errory)
	}
	fstype := "overlay"
	// flags := uintptr(syscall.MS_BIND)
	flags := uintptr(0)
	data := "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir

	var err = syscall.Mount("", mountPoint, fstype, (flags), data)
	if err != nil {
		log.Errorf("Failed to mount overlay: %w", err)
		return nil, err
	}

	d.volumesCreated[request.Name] = volume.Volume{Name: request.Name, Mountpoint: mountPoint}
	log.Debugf("Mounted volume at: %s", mountPoint)
	response := volume.MountResponse{Mountpoint: mountPoint}
	return &response, nil
}

func (d *Driver) Unmount(request *volume.UnmountRequest) error {
	err := overlay.Unmount(d.overlaysCreated[request.Name])
	if err != nil {
		return err
	}
	return nil
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
