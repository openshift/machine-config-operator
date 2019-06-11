⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCO

These instructions have been tested inside a Fedora 29 podman container (on a FSB29 host).
It should also work to run these commands directly on a host system if you haven't yet
containerized your workflow.  You will need build dependencies such as a `go` compiler,
and the image builds rely on `podman`.

## Prerequisites

1. Create a cluster using [the installer](https://github.com/openshift/installer/).  These instructions will be kept up to date generally against the leading edge of the installer.  Make sure you have set `KUBECONFIG` per the output of the installer.

1. (libvirt) Set up a local proxy

1. (libvirt) Additional registry config (see below)

The steps here come from [this comment](https://github.com/openshift/installer/issues/411#issuecomment-445165262) which provides a way for libvirt developers to expose their registry.

Allow your client binary to bind low ports:

```
sudo setcap CAP_NET_BIND_SERVICE=+eip /usr/bin/oc
```

(Or `kubectl` depending on your setup.  You may prefer to copy the `oc` binary elsewhere before writing to it as well.)


Now, forward port `443` to the router:

```
oc -n openshift-ingress port-forward svc/router-internal-default 443
```

Leave that process running in a separate terminal. In a new terminal, run step 4.

4. (all platforms) Run the cluster push prep script
```
hack/cluster-push-prep.sh
```

(Libvirt Users) You might get an error about needing to add the registry to your `/etc/hosts`; do that and then rerun the script.

## Building Components

While you're still in the mode of testing builds, use `make`:

```
$ make daemon
```

You can also `make controller` and `make operator`.

When you want to push to the cluster, use the `deploy-` prefix, e.g.:

```
make deploy-daemon
```

(Like above, you can use `operator` or `controller` in place of `daemon`).

Use `oc get pods -w` to watch for your new code to be deployed.

## Updating the Manifests

To update the manifests applied by Machine Config Operator, edit the `yaml` files in `manifests` folder in the root. The manifest folder has 3 subfolders, one for each subcomponent.

- `manifests/machineconfigcontroller`: These are all the manifests related to Machine Config Controller.
- `manifests/machineconfigdaemon`: These are all the manifests related to Machine Config Daemon.
- `manifests/machineconfigserver`: These are all the manifests related to Machine Config Server.

The `manifests` folder also contains global manifests at its root.

Every time you modify any of the manifests in `manifests` please run the following command to update the `bindata` for Machine Config Operator.

```sh
./hack/update-generated-bindata.sh
```

# Unit Tests

Unit tests (that don't interact with a running cluster) can be executed on a per
package basis with `go test`:

`go test -v github.com/openshift/machine-config-operator/pkg/apis`

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

# Turn on verbose mode for development

Given you already have a cluster up and running, you can turn verbose on (level 4)
for any given component with:

```sh
oc patch daemonset/machine-config-daemon --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--v=4"}]'
```

You can replace `daemonset/machine-config-daemon` with any other MCO component.
If the component already has a `--v=` flag set, the command above still adds
verbose and ignores the previous one already set.
If you watch the pods, you'll notice your component restarting as well.
Note this still requires [disabling the CVO](https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusterversion.md#disabling-the-cluster-version-operator).

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

# Building and installing a custom release image for testing the MCO

During MCO development there are certain test scenarios
that require a cluster running with a custom release to properly test your
code (i.e. test the interaction of the MCO with the CVO).

In those situations, you will need to:

1) build the MCO components images you're interested in testing
2) build a custom release payload
3) install a cluster with your custom release payload

## Build the MCO components images

To build the image for any component run:

```
make image-{component}
```

`{component}` can be either `operator`, `daemon`, `controller` or `server`.

After the build is complete, make sure to push the image to a registry (i.e. `podman push localhost/machine-config-{component} quay.io/user/machine-config-{component}`).

Quay.io or any other public registry isn't strictly required - you can use a local
registry as long as those images are pullable.

## Build a custom release payload

Now that your have your custom component images, to build a custom release payload, run:

```
oc adm release new -n origin --server https://api.ci.openshift.org \
                                --from-image-stream "{version number}" \
                                --to-image quay.io/user/origin-release:v{version number} \
                                machine-config-{component}=quay.io/user/machine-config-{component}:{tag}
```

`{version number}` is an openshift version, for example 4.1

There's currently a [known limitation](https://github.com/openshift/machine-config-operator/issues/421) which prevents
building a custom release payload using only a subset of the MCO components. You can work around that by
creating a payload which contains all of them:

```
oc adm release new -n origin --server https://api.ci.openshift.org \
                                --from-image-stream "{version number}" \
                                --to-image quay.io/user/origin-release:v{version number} \
                                machine-config-operator=quay.io/user/machine-config-operator:latest \
                                machine-config-controller=quay.io/user/machine-config-controller:latest \
                                machine-config-daemon=quay.io/user/machine-config-daemon:latest \
                                machine-config-server=quay.io/user/machine-config-server:latest
```

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
