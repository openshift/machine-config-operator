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

## Managing Go Dependencies

We follow a hard flattening approach; i.e. direct and inherited dependencies are installed in the base `vendor/`.

Dependencies are managed with [dep](https://golang.github.io/dep/) but committed directly to the repository. If you don't have dep, install the latest release from [Installation](https://golang.github.io/dep/docs/installation.html) link.

We require atleast following version for dep:

```
dep:
 version     : v0.5.0
 build date  : 2018-07-26
 git hash    : 224a564
 go version  : go1.10.3
```

To add a new dependency:

- Edit the `Gopkg.toml` file to add your dependency.
- Ensure you add a `version` field for the tag or the `revision` field for commit id you want to pin to.
- Revendor the dependencies:

```sh
dep ensure
```

This [guide](https://golang.github.io/dep/docs/daily-dep.html) a great source to learn more about using `dep` is .

For the sake of your fellow reviewers, commit vendored code separately from any other changes.
