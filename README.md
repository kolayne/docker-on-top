# Docker-On-Top

Docker engine plugin that implements copy-on-write mounts by using `overlayfs`.

## How to run

In the project directory, run the following commands as root:
```shell
mkdir -p /run/docker/plugins/
go build  # This 
./docker-on-top
```

This is it! While the executable is running, the plugin can be used in the docker daemon.
To check it you can run something like
```shell
docker volume create --driver docker-on-top TestVolume
```