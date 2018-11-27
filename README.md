# machine-config-operator

## Generic building

You can either use the Makefile or the `hack/build-go.sh` script directory to build the targets. When using `hack/build-go.sh` you will need to run it via `WHAT=<TARGET> hack/build-go.sh`

### machine-config-operator
- Build: `make machine-config-operator`

### machine-config-server
- [Design doc](docs/MachineConfigServer.md)
- Build: `make machine-config-server`

### machine-config-daemon
- [Design doc](docs/MachineConfigDaemon.md)
- Build: `make machine-config-daemon`

### machine-config-controller
- [Design doc](docs/MachineConfigController.md)
- Build: `make machine-config-controller`

## Tests
Tests can be executed on a per package basis with `go test` like so:

`go test -v github.com/openshift/machine-config-operator/pkg/apis`

All tests can be executed with:

`make test`

## Building Images
**NOTE**: To build images you will need [`podman`](https://github.com/containers/libpod/) installed.

Images can be built for the corresponding Dockerfiles via `make image-<TOPIC>` where `<TOPIC>` is the suffix after the first dot. For example `Dockerfile.setup-etcd-environment.rhel7` would be `make image-setup-etcd-environment.rhel7`.
