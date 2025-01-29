# onclustertesting

## Overview

Provides a very simple binary for setting up / tearing down on-cluster layering
to make testing / development go faster.

## Prerequisites
- An OpenShift cluster running 4.16+
- Kubeconfig for the aforementioned cluster
- OpenShift CLI (`oc`)
- _(optional, but recommended)_ [K9s](https://k9scli.io/)

## Usage

### Setup

Once you've installed the binary, you can set up a simple on-cluster layering
testing situation which makes use of some handy defaults such as using the
global pull secret and an in-cluster OpenShift ImageStream for pushing the
built image to.

To do this, run:
`$ onclustertesting setup in-cluster-registry --enable-feature-gate --pool=layered`

Under the hood, this will perform the following actions:
1. Verify that the appropriate feature gate is enabled, and enable it if desired.
2. Create an ImageStream within the MCO namespace called `os-image`.
3. Create a MachineConfigPool to target (defaults to `layered`) and waits for it to get an initial MachineConfig (this wait does not block the other operations, with the exception of creating a MachineOSConfig).
4. Clones the global pull secret into the MCO namespace.
5. Creates additional secrets / ConfigMaps as needed (see Other Features section below for more details). If a ConfigMap or Secret was previously created by the `onclustertesting` tool, it will be deleted and recreated.
6. Creates a MachineOSConfig for the newly-created pool once the new MachineConfigPool has an initial config, optionally adding a custom Containerfile to it.
7. The MCO should start the `machine-os-builder` pod and the build will begin. Note: `onclustertesting` will not wait for the build to complete before it exits. So you may want to use something like [K9s](https://k9scli.io/) to watch its progress.

This setup will use the in-cluster registry which requires no external
credentials to be used. The pool will start building as soon as it can. Note:
If the `TechPreviewNoUpgrade` feature gate was not previously enabled, this
will create a new MachineConfig in all MachineConfigPools, incurring a full
MachineConfig rollout before the build will start.

All objects created by the `onclustertesting` tool include the label
`machineconfiguration.openshift.io/createdByOnClusterBuildsHelper`. This
allows the tool to completely remove any and all objects that it created during
the teardown phase.

### Teardown

Assuming you have not applied any built images to your cluster nodes, one can
easily teardown everything set up by `onclustertesting` just by running the `$
onclustertesting teardown` command.

This will do the following:
1. Delete any ConfigMaps or Secrets created by the `onclustertesting` tool.
2. Delete all build objects created by the `machine-os-builder` process, including any running build pods, ConfigMaps, Secrets, etc.
3. Delete all MachineConfigPools created by the `onclustertesting` tool.
4. Delete all MachineConfigs applied to the MachineConfigPool(s) created by the `onclustertesting` tool.
5. Delete all MachineOSBuild objects.
6. Delete all MachineOSConfig objects.
7. Delete all ImageStreams created by the `onclustertesting` tool.

### Rollouts

By itself, `onclustertesting` will only test the build and push phases of the
on-cluster layering process.

To test the rollout process (applying the newly-built image to a node), you'll
need to move a given node into the MachineConfigPool created by the
`onclustertesting` program. One can do that by running the following command:

`$ onclustertesting optin --pool=<desired pool> --node=<desired node>`

There is also an optout helper that performs the inverse operation:

`$ onclustertesting optout --pool=<desired pool> --node=<desired node>`

Alternatively, you can use `oc` or `kubectl` to perform the same action:

`$ oc label node/<desired-node> -l 'node-role.kubernetes.io/<name-of-machineconfigpool>='`

It is worth mentioning that once a node is opted in, using `onclustertesting`'s
teardown process may leave your cluster in a difficult-to-recover-from state.
This is mostly because the revert feature does not work yet. Once that is
feature is implemented in OCL, this should no longer be a problem.

## Other features

### RHEL entitlements

If your cluster has the `etc-pki-entitlement` secret in the
`openshift-config-managed` namespace, the operator will automatically
copy it into the MCO namespace, when a build is required.

### /etc/yum.repos.d and /etc/pki/rpm-gpg

If you want to test the `/etc/yum.repos.d` and `/etc/pki/rpm-gpg` injection
capabilities of on-cluster layering, you can provide the `--inject-yum-repos`
flag with the `setup` command. Doing this will cause the following to occur:

1. `onclustertesting` will use the `oc`
command to extract the `/etc/yum.repos.d` and `/etc/pki/rpm-gpg` directories
from a container image found at `quay.io/zzlotnik/devex:epel`. For speed, this
image only contains those directories and is built using [this
Containerfile](https://github.com/cheesesashimi/containerfiles/commit/33ddc71dc3f480055cfb41f3c6a568c70d4d81da#diff-79251285e7d83f5a8acf208fdb543c6f7ac5c2af3d533cb0458d6c4010a5d4d3).
2. These files will be placed in a temporary directory before being converted into a ConfigMap and Secret.
3. The ConfigMap `etc-yum-repos-d` and the Secret `etc-pki-rpm-gpg` will be created within the MCO namespace.
4. The temporary directory will be removed.

These objects will be removed during the teardown process.

### Custom Containerfiles

During the setup process, you can optionally inject a custom Containerfile by
supplying the `--containerfile-path` flag and a path to a Containerfile present on
your machine.

The Containerfile will be read and injected into the MachineOSConfig for the
MachineConfigPool that the tool creates.

**Note:** The custom Containerfile must include a stage beginning with `FROM
configs AS final` in order for your customizations to be built into the final image.

### CI mode

CI mode is a new subcommand under the `setup` command, which is intended to opt
a given cluster into on-cluster layering so that a test suite (such as
`openshift-e2e`) may be run against it. Using this command does the following:

1. Creates two ImageStreams; one for the control-plane and one for the worker pool.
2. Clones the global pull secret into the MCO namespace.
3. Creates a MachineOSConfig for both the control-plane and worker pool. Then waits for the builds to complete.
4. Waits for the newly-built image to roll out to each node in both the control-plane and worker pools.
