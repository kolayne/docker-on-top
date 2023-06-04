package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// internalError wraps the given error in the "docker-on-top internal error: #{help}: #{err}" message. It is useful for
// when the error is reported to the docker daemon so that the end user knows it's not their mistake but an internal
// error.
func internalError(help string, err error) error {
	// Maybe make a custom error type instead?
	return fmt.Errorf("docker-on-top internal error: %s: %w", help, err)
}

// DockerOnTop contains internal data of the docker-on-top volume driver and implements the `volume.Driver` interface
// (from docker's go-plugins-helpers).
//
// **MUST BE** created with `NewDockerOnTop` only.
type DockerOnTop struct {
	// dotRootDir is the base directory of docker-on-top, where all the internal information is stored.
	// Must contain a trailing slash.
	dotRootDir string
}

// NewDockerOnTop creates a new `DockerOnTop` object using the given directory as the dot root directory. If it doesn't
// exist, it is created as in `mkdir -p`. On error, it is returned and `DockerOnTop` is not created.
func NewDockerOnTop(dotRootDir string) (*DockerOnTop, error) {
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

	entries, err := os.ReadDir(dotRootDir)
	if err != nil {
		return nil, err
	}

	dot := DockerOnTop{dotRootDir: dotRootDir}
	wereMountedCount := 0
	for _, entry := range entries {
		volumeName := entry.Name()
		err = dot.volumeTreeOnBootReset(volumeName)
		if err == nil {
			log.Infof("Detected volume %s. The state was dirty, cleaned successfully", volumeName)
		} else if os.IsNotExist(err) {
			log.Infof("Detected volume %s. The state is clean", volumeName)
		} else if errors.Is(err, syscall.EBUSY) {
			log.Infof("Detected volume %s. The state is dirty: it is still mounted", volumeName)
			wereMountedCount++
		} else {
			log.Errorf("Failed to reset volume %s on boot: %v", volumeName, err)
			return nil, err
		}
	}

	if wereMountedCount > 0 {
		// Not sure which message is better, keeping both for now
		/*
			log.Warning("Some of the detected volumes (mentioned above as INFO logs) were already mounted when the " +
				"plugin started. If some of the containers using it have exited and there's been over 60sec after that " +
				"while the plugin was down, those volumes are now stuck in the mounted state until you reboot your " +
				"machine. For non-volatile volumes it's not too bad, for volatile volumes it means their changes won't " +
				"be discarded on container exit (they effectively lose their volatility until a reboot).")
		*/
		log.Warning("Some of the detected volumes were already mounted when the plugin started. If the " +
			"plugin's downtime was <=60sec or you know that no containers with mounted dirty volumes have exited " +
			"while the plugin was down, there's no problem. Otherwise the aforementioned (as INFO logs) volumes " +
			"might get stuck in the mounted state, and for volatile volumes it prevents their changes from being " +
			"discarded. In any case, the machine reboot will fix everything")
	}

	return &dot, nil
}

// MustNewDockerOnTop behaves as `NewDockerOnTop` but panics in case of an error
func MustNewDockerOnTop(baseDir string) *DockerOnTop {
	driver, err := NewDockerOnTop(baseDir)
	if err != nil {
		panic(fmt.Errorf("the call NewDockerOnTop(%+v) failed: %v", baseDir, err))
	}
	return driver
}
