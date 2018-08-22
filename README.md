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