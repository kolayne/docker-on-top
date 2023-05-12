package main

import (
	"errors"
	"github.com/docker/go-plugins-helpers/volume"
)

// Driver contains internal data for the docker-on-top volume driver. It does not export any fields
type Driver struct {
	// TODO: some fields will be here. For now it's just a stub implementation
	volumesCreated map[string]bool // In this implementation the value does not mean anything, only the key
}

// Use https://docs.docker.com/engine/extend/plugins_volume/ as a reference for your implementation

func (d *Driver) Create(request *volume.CreateRequest) error {
	log.Debugf("Request Create: Name=%s Options=%s", request.Name, request.Options)

	if _, ok := d.volumesCreated[request.Name]; ok {
		log.Debug("Volume already exists. Refusing to create one")
		return errors.New("volume already exists")
	} else {
		log.Debugf("Pretending that the volume was successfully created")
		d.volumesCreated[request.Name] = true
		return nil
	}
}

func (d *Driver) List() (*volume.ListResponse, error) {
	log.Debug("Request List")

	var response volume.ListResponse
	for volumeName, _ := range d.volumesCreated {
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
	delete(d.volumesCreated, request.Name)
	return nil
}

func (d *Driver) Path(request *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Request Path: Name=%s", request.Name)
	log.Error("Path is not implemented. Returning an error")
	return nil, errors.New("the Path request is not implemented")
}

func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)
	log.Error("Mount is not implemented. Returning an error")
	return nil, errors.New("the Mount request is not implemented")
}

func (d *Driver) Unmount(request *volume.UnmountRequest) error {
	log.Debugf("Request Unmount: ID=%s, Name=%s", request.ID, request.Name)
	log.Error("Unmount is not implemented. Returning an error")
	return errors.New("the Unmount request is not implemented")
}

func (d *Driver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug("Request Capabilities: plugin discovery")
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "volume"}}
}
