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
execution in [kubensenter](../kubensenter/) or nsenter(1).

Example nsenter usage:

```
ExecStart=nsenter --mount=/run/kubens/mnt mycommand --option
```
