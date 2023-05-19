package main

import (
	"github.com/docker/go-plugins-helpers/volume"
	"github.com/op/go-logging"
)

// TODO: set logging format
var log = logging.MustGetLogger("docker-on-top")

func main() {
	log.Info("Hello there!")

	handler := volume.NewHandler(&Driver{volumesCreated: map[string]volume.Volume{}, overlaysCreated: map[string]string{}})
	log.Info(handler.ServeUnix("/run/docker/plugins/docker-on-top.sock", 0))
}
