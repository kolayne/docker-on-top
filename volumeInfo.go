package main

import (
	"encoding/json"
	"os"
)

type VolumeInfo struct {
	BaseDirPath string
	Volatile    bool
}

func (d *DockerOnTop) metadatajson(volumeName string) string {
	return d.dotRootDir + volumeName + "/metadata.json"
}

func (d *DockerOnTop) getVolumeInfo(volumeName string) (VolumeInfo, error) {
	var vol VolumeInfo

	payload, err := os.ReadFile(d.metadatajson(volumeName))
	if err == nil {
		err = json.Unmarshal(payload, &vol)
	}

	return vol, err
}

func (d *DockerOnTop) writeVolumeInfo(volumeName string, vol VolumeInfo) error {
	payload, err := json.Marshal(vol)

	if err == nil {
		err = os.WriteFile(d.metadatajson(volumeName), payload, 0o666)
	}

	return err
}
