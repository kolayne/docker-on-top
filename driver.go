package main

import (
	"errors"
	"github.com/containers/buildah/pkg/overlay"
	"github.com/docker/go-plugins-helpers/volume"
	"os"
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

/*
TEST:
sudo docker volume create --driver docker-on-top test101
sudo docker run -name ubuntu_bash_101 --mount type=volume,source=test101,destination=/usr -t -i --privileged ubuntu bash
cd ./usr
> nex.txt #check if this file appears in overlay_path/upper directory
sudo docker rm ubuntu_bash_101
sudo docker volume rm test101
# check if the /home/VolumeOverlays/test101 was deleted
*/
func (d *Driver) Mount(request *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Request Mount: ID=%s, Name=%s", request.ID, request.Name)
	path := "/home/VolumeOverlays/" + request.Name
	err1 := os.MkdirAll(path+"/lower", os.ModePerm)
	err2 := os.MkdirAll(path+"/upper", os.ModePerm)
	err3 := os.MkdirAll(path+"/work_dir", os.ModePerm)
	if err1 != nil || err2 != nil || err3 != nil {
		err := errors.Join(err1, err2, err3)
		log.Errorf("Failed to mkdir internal overlay directories (lower, upper, work_dir): %w", err)
		return nil, err
	}
    _, err := overlay.Mount(path, path+"/lower", path+"/upper", 0, 0, []string{"lowerdir=" + path + "/lower,upperdir=" + path + "/upper,work_dir=" + path + "/work_dir"})
	if err != nil {
		log.Error("ERROR in overlay:")
		log.Error(err)
		return nil, err
	}
	d.volumesCreated[request.Name] = volume.Volume{Name: request.Name, Mountpoint: path + "/upper"}
	log.Debugf("Get Mountpoint of Volume: " + d.volumesCreated[request.Name].Mountpoint)
	response := volume.MountResponse{Mountpoint: d.volumesCreated[request.Name].Mountpoint}
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
