# systemd Integration

To integrate this library with systemd-managed services, we need two things to happen:

## Create the namespace in a stand-alone service

The `kubens.service` will create a properly-configured mount namespace and bind
it to `/run/kubens/mnt`.  This mount namespace must be:
- A slave to the host OS so that host OS mounts are available to kubelet, the
  container runtime, and containers.
- Recursively shared within itself, so that containers sharing their own
  mountpoints back out to other containers (such as CSI containers) get their
  mounts properly propagated back out to the container runtime and down into
  other containers.

## Configure all services that need access to join the new mount namespace

For golang services that include this github.com/containers/kubensmnt library,
this is as simple as adding the `kubens-dropin.conf` to the service. This
drop-in will introduce an After relationship to `kubens.service` and include
the appropriate environment variable if the service is running.

For other services that do not use the kubensmnt library, you can wrap their
execution in [kubensenter](../kubensenter/README.md) or nsenter(1).

Example nsenter usage:

```
ExecStart=nsenter --mount=/run/kubens/mnt mycommand --option
```

# Installation

We recommend running the installation scripts from the [parent utils
directory](../README.md)

However, you can install only the systemd service and drop-ins at
this level:

```
sudo make install [WRAP_SERVICES=...] [ENV_SERVICES=...]
```

This will install `kubens.service` and enable it to run at boot.

Adding WRAP_SERVICES=... will also create drop-in wrappers
which wraps each service's ExecStart line with kubensenter:
```
sudo make install WRAP_SERVICES="k3s.service other.service"
```

Adding ENV_SERVICES=... will also create drop-ins which set
$$KUBENSMNT for use with services that can enter the mount
namespace on their own:
```
make install ENV_SERVICES="crio.service"
```

Wrapping services in this manner requires that the
[kubensenter](../kubensenter/README.md) script is already installed,
which is why installation from the parent directory (which does both)
is recommended.

## Go vendoring

If you are using vendored go modules, you may also include thesr
files via go embed inclusion. Just set up a tools.go like this:

```go
//go:build tools
// +build tools

// tools is a dummy package that will be ignored for builds, but included for dependencies.
package tools

import (
	_ "github.com/containers/kubensmnt/utils/systemd"
)
```

Then, as usual, run:
```bash
go get github.com/containers/kubensmnt@latest # or some tag
go mod tidy
go mod vendor
```

The systemd utils content will be in your local vendor/github.com/containers/kubensmnt/utils/systemd/ directory.
