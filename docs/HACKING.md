⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCO

These instructions have been tested inside a Fedora 31 toolbox (podman) container (on a Fedora Silverblue 31 host).
It should also work to run these commands directly on a host system. You will need build dependencies such as a `go` compiler,
and the image builds rely on `podman`.

## Prerequisites

Most changes you'll want to test live against a real cluster.  Use [the installer](https://github.com/openshift/installer/), and in particular you'll want to use the "latest CI" release of the installer.  As of the time of this writing, that's 4.5.  More information on the [release page](https://openshift-release.svc.ci.openshift.org/).

Go v1.16 is used for development of the MCO.

These instructions will be kept up to date generally against the leading edge of the installer.  Make sure you have set `KUBECONFIG` per the output of the installer.

## Building Components

While you're still in the mode of testing builds, use `make`:

```
$ make machine-config-daemon
```

You can also `make machine-config-controller` and `make machine-config-operator`, or just `make binaries` to build all of them.

See below for [deploying changes to a live cluster](#running-in-a-cluster).

## Updating the Manifests

To update the manifests applied by Machine Config Operator, edit the `yaml` files in `manifests` folder in the root. The manifest folder has 3 subfolders, one for each subcomponent.

- `manifests/machineconfigcontroller`: These are all the manifests related to Machine Config Controller.
- `manifests/machineconfigdaemon`: These are all the manifests related to Machine Config Daemon.
- `manifests/machineconfigserver`: These are all the manifests related to Machine Config Server.

The `manifests` folder also contains global manifests at its root.

# Unit Tests

Unit tests (that don't interact with a running cluster) can be executed on a per
package basis with `go test`:

`go test -v github.com/openshift/machine-config-operator/pkg/apis/...`

To disable go test caching in go > 1.10:

`go test -count=1 ...`

To execute all unit tests:

`make test-unit`

All tests (unit and e2e) can be executed with:

`make test`

# Managing Go Dependencies

Dependencies are managed with [go modules](https://github.com/golang/go/wiki/Modules) but committed directly to the repository.

Please ensure that you are using Go v1.16.  Using a different version may result in unexpected behavior.

After updating the go code using `import` statements to reference the desired new packages, add the new dependencies by running:

```sh
make go-deps
```
For the sake of your fellow reviewers, commit vendored code separately from any other changes.

# Developing the MCD without building images

It is possible to iterate on the MCD without having to rebuild images for each
change. You can use the `hack/prep-cluster-for-mcd-push.sh` to prepare the
cluster to push your MCD changes. You only need to run this script once, but
running it additional times is fine. To build and push your changes, use the
`hack/push-to-mcd-pods.sh` script. The scripts do the following:

`hack/prep-cluster-for-mcd-push.sh`:
1. Disables the cluster version operator and the machine config operator. This prevents the MCD DaemonSet modifications the script makes from being overwritten.
1. Copies the MCD binary from the container onto the host's filesystem via the `/rootfs` volume mount on the MCD pods.
1. Modifies The MCD DaemonSet to compute the sha256sum of the binary and then start the MCD binary from the nodes' filesystems instead of from inside the container. The sha256sum is useful for determining if the latest MCD binary is running.
1. Restarts MCD DaemonSet to use the new config.

To revert the cluster back to its prior configuration, reenable the cluster version operator and the machine config operator:
- `$ oc scale --replicas=1 deployment/machine-config-operator --namespace openshift-machine-config-operator`
- `$ oc scale --replicas=1 deployment/cluster-version-operator --namespace openshift-cluster-version`

`hack/push-to-mcd-pods.sh`:
1. Determines the underlying cluster architecture and OS. **NOTE:** It only grabs this info from the first node returned by `$ oc get nodes`. This will need some refactoring for multiarch clusters.
1. Builds the MCD binary for the target cluster architecture and OS by setting the `GOOS` and `GOARCH` env variables.
1. Computes a sha256sum of the newly-built MCD binary.
1. Copies the newly-built binary to the nodes' underlying filesystem via the currently-running MCD pods.
1. Restarts the MCD DaemonSet which causes the newly-built MCD binary to be executed.

`hack/get-mcd-pods.py`:

This script outputs the currently running MCD pods, what nodes they're running on, and what roles they have:

```console
Current MCD Pods:
machine-config-daemon-9l5nm     ip-10-0-171-232.ec2.internal    master
machine-config-daemon-dsknp     ip-10-0-137-167.ec2.internal    worker
machine-config-daemon-mrsml     ip-10-0-161-151.ec2.internal    worker
machine-config-daemon-rdlgn     ip-10-0-152-65.ec2.internal     master
machine-config-daemon-t6qnm     ip-10-0-155-187.ec2.internal    worker
machine-config-daemon-wrh6c     ip-10-0-130-41.ec2.internal     master
```


# The test suites

We have a few contexts that run on pull requests. `unit` runs `make test-unit`.
The `e2e-aws` job is common across most OpenShift repos; it runs the installer
with a custom update payload using code from the PR, and then runs the e2e test
suite from [OpenShift Origin](https://github.com/openshift/origin/).

[Recently we added](https://github.com/openshift/release/pull/2577) an
`e2e-aws-op` job. This one also generates a cluster with the code, but runs
`make test-e2e` from our own repo rather than Origin's test suite. We can run
destructive tests here.

## Debugging a test suite

When a PR is first created, Prow will create a Kubernetes namespace (or possibly
reuse an existing one).  Click on "... Skipping 47 lines ..." to expand it, and
you will see a line like: `2019/02/13 18:56:22 Using namespace ci-op-ydnn8xvi`.
The different parts of a build/test run are all inside that namespace that
runs in the https://api.ci.openshift.org/ cluster.

When CI is complete you'll see a lot of information more nicely rendered, including
artifacts. The `pods/` directory for example has the logs for the various
pods.  Lists of some important objects are extracted to JSON; for example `nodes.json` has
a list of the node state.  For this project, the `machineconfigs.json` and `machineconfigpools.json`
are commonly useful.

## Logging into a live test cluster

Login: `oc login --server=https://api.ci.openshift.org`

In the pull request, you'll see a "project" or Kubernetes namespace, so you can e.g.: `oc project ci-op-zgctgrpy`
You can watch the installer logs via `oc logs -f -c setup e2e-aws`.

Now, you can get the Kubernetes credentials back to your machine:

```sh
oc rsh -c test e2e-aws cat /tmp/artifacts/installer/auth/kubeconfig > $XDG_RUNTIME_DIR/kubeconfig
export KUBECONFIG=$XDG_RUNTIME_DIR/kubeconfig
```

And from there debug the live cluster while the tests are running. Though be
aware that it will be quickly torn down as soon as the tests have completed.

# Running in a cluster

During MCO development there are certain test scenarios
that require a cluster running with a custom release to properly test your
code (i.e. test the interaction of the MCO with the CVO).

In those situations, you will need to:

1) build the MCO components images you're interested in testing
2) build a custom release payload
3) install a cluster with your custom release payload

## Build MCO image

To build an image that contains all MCO components (mcc/mcd/mco/mcs/setup-etcd-env) run:

```
make image
```

This script keys off the git commit which gets embedded as the standard `vcs-ref`
image label, so if you want to deploy new changes, you'll need to `git commit`.

After the build is complete, make sure to push the image to a registry (i.e. `podman push localhost/machine-config-operator quay.io/user/machine-config-operator`).

You can also use the script [hack/push-image.sh](hack/push-image.sh) to push the image
generated in `make image` to the container registry of your choice. For example, after logging
in via the command-line:

```
REPO={docker.io/username} ./hack/push-image.sh
```

Quay.io or any other public registry isn't strictly required - you can use a local
registry as long as those images are pullable.

By default, podman created quay.io repo is marked as private, make sure to
change it in the quay.io webapge to public, allowing the cluster to pull
the update payload.

## Build a custom release payload

Get a pull secret from https://console.redhat.com/openshift/install/pull-secret
and store it to `$HOME/.kube/pull-secret.txt` or any place you like.

Besides pull secret, you need to run `podman login quay.io:443` and `oc
registry login`. The reason to use `quay.io:443` is because pull secret already
has `quay.io` login information for
`quay.io/openshift-release-dev/ocp-release`, but you need your personal account
to upload to `quay.io:443/user/origin-release`.

Now that your have your custom image, to build a custom release payload, run:

```
oc adm release new \
    -a ~/.kube/pull-secret.txt \
    --from-release \
    quay.io/openshift-release-dev/ocp-release:{version_number}-x86_64 \
    --name {version_number} \
    --to-image quay.io:443/<your_user>/origin-release:v{version_number} \
    machine-config-operator=quay.io/<your_user>/machine-config-operator:latest
```

`{version_number}` is an openshift version, for example 4.13.21

Make sure you're using a relatively new `oc` binary from `openshift/oc`. The image must be pullable by
remote resources (nodes), therefore using a local registry might not work.

If you run into authentication problem, please check these paths:
 * `${XDG_RUNTIME_DIR}/containers/auth.json`
 * `$HOME/.config/containers/auth.json`
 * `$HOME/.docker/config.json`

Please check `quay.io/<your-user>/origin-release` webpage to make sure it is
public accessible.

When the command above finishes, your custom release payload is going to be available
at the location you specified via the `--to-image` flag for the installer to be consumed. E.g.:

```
oc adm upgrade --force --to-image quay.io/<your_user>/origin-release:v{version number}
```

To watch the upgrade you can do:

```
watch oc get clusterversion
```

Note for quay.io users: images pushed to your personal account are going to be private by default.
If you want to keep the release payload image private you will need to provide
the secret to pull it in the `install-config.yaml` configuration (`pullSecret` section specifically).

## Install a cluster with a custom release payload

In order to use your new custom release payload to install a new cluster, simply run the creation process with
the `OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE` environment variable like so:

```
OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE=quay.io/user/origin-release:v{version number} bin/openshift-install create cluster --log-level=debug
```

## Quickly deploying changes without upgrading

Doing the "push container, new release image" flow can be slow.  We also have
an alternative workflow:

Do this once:

```
$ ./hack/cluster-push-prep.sh
```

Note this will scale down the CVO and hence disable upgrades.

Then, to directly push your images, run:
```
$ ./hack/cluster-push.sh
```

## Hacking on `rhel-coreos` image

If you own part of the operating system (from kernel to kubelet) you
are part of the `rhel-coreos` image.  More information in [OSUpgrades.md](OSUpgrades.md).
You will want a workflow for testing changes to a cluster.

### Directly applying changes live to a node

The simplest workflow is to use `oc debug node/` to start testing your changes on a single worker node.
Commands to use here include `rpm-ostree usroverlay` and/or `rpm-ostree override replace`.
The first one gives you a writable overlayfs on `/usr` and you can easily
replace binaries there (e.g. `/usr/bin/crio`).  For anything that requires a reboot
(particularly the `kernel` package) you'll need to use `rpm-ostree override replace`.

### Applying a custom oscontainer

With OCP 4.12+, we are using `rhel-coreos` which is an OCI container image and
can be used as a base image to create custom container.
The easiest way to create and apply custom image is using [layering](https://docs.openshift.com/container-platform/4.15/post_installation_configuration/coreos-layering.html).

A more advanced flow is to build a custom `rhel-coreos`
container; this exercises applying updates the same way that is used by
the default upgrade path.  This can be useful if for example you're testing
code related to upgrades.  For this, see https://github.com/coreos/coreos-assembler/pull/489
(A future iteration of this document will better describe this part)
But let's assume you have a custom container and have pushed to a registry, for
this example `quay.io/example/rhel-coreos:latest`.

If you want to roll it out to the entire cluster using the MCO, first scale down the CVO:
`oc -n openshift-cluster-version scale --replicas=0 deploy/cluster-version-operator`.

Then,
`oc -n openshift-machine-config-operator edit configmap/machine-config-osimageurl`
and change the `osImageURL: quay.io/example/rhel-coreos@sha256:...`.
Notice the use of the pull-by-digest form `@sha256`; this is required by the MCO.

This will follow the upgrade process that's normally used for upgrades and only
drain/reboot a single node at a time.

### Replacing `rhel-coreos` content in a new release image

The method that best matches the way true upgrades work though is to build
a custom release image that includes your custom `rhel-coreos` as an
override.  To do this, follow the instructions above for creating a custom
release image, but instead of overriding `machine-config-operator`, override
`rhel-coreos`.  Note that today the MCO code requires that the OS
come from a "digested" pull spec, e.g.
`oc adm release new ... rhel-coreos=quay.io/user/rhel-coreos@sha256:49aefeabe1459e4091859b89ac1bc43d4161296cf80113fb633d59a56018ffa6`.
It will fail if you use e.g. `:latest`.

At the time of this writing, the [kubelet](https://github.com/smarterclayton/origin/blob/4de957b019aee56931b1a29af148cf64865a969b/images/os/Dockerfile)
has code to do this in CI, and work is in progress to [replicate that for cri-o](https://github.com/openshift/release/pull/4030).

### Running worker nodes with customized kubelet

1) Ssh to the node (use github.com/eparis/ssh-bastion)
``
$ ssh core@<worker-node-ip>
``
2) Use scp to copy it the customised kubelet binary
``
$ scp root@<remote-host-with-kubelet-binary>:<path/to/kubelet/binary> /home/core
``
3) Obtain a writable overlayfs on /usr
``
rpm-ostree usroverlay
``
4) ``$ systemctl stop kubelet``
5) Replace kubelet binary (/usr/bin/kubelet). Please refer [here](https://github.com/openshift/machine-config-operator/blob/master/docs/HACKING.md#directly-applying-changes-live-to-a-node) for more detail.
6) In certain cases, changes might be needed to the service file (/etc/systemd/system/kubelet.service)
   depending on the version of kubelet you are running. As an example, changing kubelet binary from
   v1.18.3 to v1.13.0-alpha, it was necessary to remove `--node-labels=node-role.kubernetes.io/worker,node.openshift.io/os_id=${ID}`
   because as per upstream kubelet binary node Labels must begin with an allowed prefix (kubelet.kubernetes.io, node.kubernetes.io) or be in the specifically allowed set (beta.kubernetes.io/arch, beta.kubernetes.io/instance-type, beta.kubernetes.io/os, failure-domain.beta.kubernetes.io/region, failure-domain.beta.kubernetes.io/zone, kubernetes.io/arch, kubernetes.io/hostname, kubernetes.io/os, node.kubernetes.io/instance-type, topology.kubernetes.io/region, topology.kubernetes.io/zone).
7) ``$ systemctl daemon-reload``
8) ``$ systemctl restart kubelet``
9) ``$ oc get nodes`` to see the updated kubelet version

## Accessing the MachineConfigServer Directly

Sometimes you want to be able to make raw `curl` connections to the MachineConfigServer (MCS)
during testing (i.e. https://github.com/openshift/machine-config-operator/pull/1960).

1) Access one of the nodes
2) Adjust the `iptables` rules
3) Use `curl` to talk to the MCS port (22623)

```bash
$ oc debug node/ip-10-0-147-70.ec2.internal
Starting pod/ip-10-0-147-70ec2internal-debug ...
To use host binaries, run `chroot /host`
Pod IP: 10.0.147.70
If you don't see a command prompt, try pressing enter.
sh-4.2# chroot /host
sh-4.4# /sbin/iptables -nL FORWARD --line-numbers | grep -E '2262[34]' | awk '{print $1}' | xargs -n 1 echo | tac | xargs -n 1 /sbin/iptables -D FORWARD
sh-4.4# curl -k https://<api-server-url>:22623/config/worker
...
sh-4.4# curl -H "Accept: application/vnd.coreos.ignition+json; version=3.2.0" -k https://<api-server-url>/config/worker
...
```

See the [MachineConfigServer](MachineConfigServer.md) docs for more information.
