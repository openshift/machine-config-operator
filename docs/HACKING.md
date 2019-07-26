⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCO

These instructions have been tested inside a Fedora 30 toolbox (podman) container (on a FSB30 host).
It should also work to run these commands directly on a host system if you haven't yet
containerized your workflow.  You will need build dependencies such as a `go` compiler,
and the image builds rely on `podman`.

## Prerequisites

Most changes you'll want to test live against a real cluster.  Use [the installer](https://github.com/openshift/installer/), and in particular you'll want to use the "latest CI" release of the installer.  As of the time of this writing, that's 4.2.  More information on the [release page](https://openshift-release.svc.ci.openshift.org/).

These instructions will be kept up to date generally against the leading edge of the installer.  Make sure you have set `KUBECONFIG` per the output of the installer.

## Building Components

While you're still in the mode of testing builds, use `make`:

```
$ make daemon
```

You can also `make controller` and `make operator`, or just `make binaries` to build all of them.

See below for [deploying changes to a live cluster](#running-in-a-cluster).

## Updating the Manifests

To update the manifests applied by Machine Config Operator, edit the `yaml` files in `manifests` folder in the root. The manifest folder has 3 subfolders, one for each subcomponent.

- `manifests/machineconfigcontroller`: These are all the manifests related to Machine Config Controller.
- `manifests/machineconfigdaemon`: These are all the manifests related to Machine Config Daemon.
- `manifests/machineconfigserver`: These are all the manifests related to Machine Config Server.

The `manifests` folder also contains global manifests at its root.

Every time you modify any of the manifests in `manifests` please run the following command to update the `bindata` for Machine Config Operator.

```sh
make update
```

# Unit Tests

Unit tests (that don't interact with a running cluster) can be executed on a per
package basis with `go test`:

`go test -v github.com/openshift/machine-config-operator/pkg/apis`

To disable go test caching in go > 1.10:

`go test -count=1 ...`

To execute all unit tests:

`make test-unit`

All tests (unit and e2e) can be executed with:

`make test`

# Managing Go Dependencies

We follow a hard flattening approach; i.e. direct and inherited dependencies are installed in the base `vendor/`.

Dependencies are managed with [dep](https://golang.github.io/dep/) but committed directly to the repository. If you don't have dep, install the latest release from [Installation](https://golang.github.io/dep/docs/installation.html) link.

We require at least the following versions for dep:

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

# Developing the MCD without building images

It is possible to iterate on the MCD without having to rebuild images
for each change. To do this, change the daemonset definition to
something like:


```yaml
containers:
- command: ["/bin/bash"]
  args:
  - -c
  - cp /rootfs/usr/local/bin/machine-config-daemon /usr/local/bin/machine-config-daemon && /usr/local/bin/machine-config-daemon start -v 4
```

Then, one can simply `scp` newly built binaries to `/usr/local/bin` on
all the nodes and restart the daemon (one can just delete the running
ones and let new instances take their place). E.g.:

```sh
# copy core creds to root so we can scp directly in the next invocation
for ip in 11 51; do ssh core@192.168.126.$ip sudo cp -R /home/core/.ssh /root; done
# scp MCD build to /usr/local/bin
for ip in 11 51; do scp _output/linux/amd64/machine-config-daemon root@192.168.126.$ip:/usr/local/bin; done
```

This still requires [disabling the CVO](https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusterversion.md#disabling-the-cluster-version-operator). It also requires disabling
the operator since the MCD daemonset will be overwritten:

```
oc scale deployment machine-config-operator --replicas=0
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
REPO={docker.io/username} /hack/push-image.sh
```

Quay.io or any other public registry isn't strictly required - you can use a local
registry as long as those images are pullable.

## Build a custom release payload

Now that your have your custom image, to build a custom release payload, run:

```
oc adm release new -n origin --server https://api.ci.openshift.org \
                                --from-image-stream "{version number}" \
                                --to-image quay.io/user/origin-release:v{version number} \
                                machine-config-operator=quay.io/user/machine-config-operator:latest
```

`{version number}` is an openshift version, for example 4.1

Make sure you're using a relatively new `oc` binary from `openshift/origin`. The image must be pullable by
remote resources (nodes), therefore using a local registry might not work.

Any registry credentials need to be present in `~/.docker/config.json` for `oc` to interact with the registry.

When the command above finishes, your custom release payload is going to be available
at the location you specified via the `--to-image` flag for the installer to be consumed.

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

## Hacking on `machine-os-content`

If you own part of the operating system (from kernel to kubelet) you
are part of the `machine-os-content`.  More information in [OSUpgrades.md](OSUpgrades.md).
You will want a workflow for testing changes to a cluster.

### Directly applying changes live to a node

The simplest workflow is to `use oc debug node/` to start testing your changes on a single worker node.
Commands to use here include `rpm-ostree usroverlay` and/or `rpm-ostree override replace`.
The first one gives you a writable overlayfs on `/usr` and you can easily
replace binaries there (e.g. `/usr/bin/crio`).  For anything that requires a reboot
(particularly the `kernel` package) you'll need to use `rpm-ostree override replace`.

### Applying a custom oscontainer

A more advanced flow is to build a custom `machine-os-content`
container; this exercises applying updates the same way that is used by
the default upgrade path.  This can be useful if for example you're testing
code related to upgrades.  For this, see https://github.com/coreos/coreos-assembler/pull/489
(A future iteration of this document will better describe this part)
But let's assume you have a custom container and have pushed to a registry, for
this example `quay.io/example/machine-os-content:latest`.

Once you have an oscontainer, you can again use `oc debug node/` and  `pivot` to directly switch
to the target oscontainer, e.g. `pivot quay.io/example/machine-os-content:latest`.
If you choose this path though the MCD will go degraded until you revert the change.

If you want to roll it out to the entire cluster using the MCO, first scale down the CVO:
`oc -n openshift-cluster-version scale --replicas=0 deploy/cluster-version-operator`.

Then,
`oc -n openshift-machine-config-operator edit configmap/machine-config-osimageurl`
and change the `osImageURL: quay.io/example/machine-os-content@sha256:...`.
Notice the use of the pull-by-digest form `@sha256`; this is required by the MCO.

This will follow the upgrade process that's normally used for upgrades and only
drain/reboot a single node at a time.

### Replacing `machine-os-content` in a new release image

The method that best matches the way true upgrades work though is to build
a custom release image that includes your custom `machine-os-content` as an
override.  To do this, follow the instructions above for creating a custom
release image, but instead of overriding `machine-config-operator`, override
`machine-os-content`.

At the time of this writing, the [kubelet](https://github.com/smarterclayton/origin/blob/4de957b019aee56931b1a29af148cf64865a969b/images/os/Dockerfile)
has code to do this in CI, and work is in progress to [replicate that for cri-o](https://github.com/openshift/release/pull/4030).
