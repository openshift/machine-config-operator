# machine-config-operator

## machine-config-operator
- Build: `WHAT=machine-config-operator ./hack/build-go.sh`

## machine-config-server
- [Design doc](docs/MachineConfigServer.md)
- Build: `WHAT=machine-config-server ./hack/build-go.sh`

## machine-config-daemon
- [Design doc](docs/MachineConfigDaemon.md)
- Build: `WHAT=machine-config-daemon ./hack/build-go.sh`

## machine-config-controller
- [Design doc](docs/MachineConfigController.md)
- `WHAT=machine-config-controller ./hack/build-go.sh`

## Tests
Tests can be executed on a per package basis with `go test` like so:

`go test -v github.com/openshift/machine-config-operator/pkg/apis`

All tests can be executed with:

`go test -v ./...`

## Building Images
**NOTE**: To build images you will need [`podman`](https://github.com/containers/libpod/) installed.

Images can be built locally via the `hack/build-image.sh` script. This script uses the
`WHAT` variable similarly to the `hack/build-go.sh` script.

```console
$ ./hack/build-image.sh
ERROR: Note: Building unprivileged may fail due to permissions
ERROR: WHAT must be set to one of the following:
ERROR: - all
ERROR: - machine-config-controller
ERROR: - machine-config-daemon
ERROR: - machine-config-operator
$ WHAT=machine-config-daemon ./hack/build-image.sh
[...]
```
