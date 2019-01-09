⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCO - prep

These instructions have been tested inside a Fedora 29 podman container (on a FSB29 host).
It should also work to run these commands directly on a host system if you haven't yet
containerized your workflow.  You will need build dependencies such as a `go` compiler,
and the image builds rely on `podman`.

1. Create a cluster using [the installer](https://github.com/openshift/installer/).  Many of the MCD developers use libvirt.  These instructions will be kept up to date generally against the leading edge of the installer.  Make sure you have set `KUBECONFIG` per the output of the installer.

1. (libvirt) Set up a local proxy

The steps here come from [this comment](https://github.com/openshift/installer/issues/411#issuecomment-445165262) which provides a way for libvirt developers to expose their registry.

Allow your client binary to bind low ports:

```
sudo setcap CAP_NET_BIND_SERVICE=+eip /usr/bin/oc
```

(Or `kubectl` depending on your setup.  You may prefer to copy the `oc` binary elsewhere before writing to it as well.)

Now, forward port `443` to the router:

```
oc -n openshift-ingress port-forward svc/router-default 443
```

Leave that process running in a separate terminal.

1. Run `hack/cluster-push-prep.sh` (once)

You will likely get an error about needing to add the registry to your `/etc/hosts`; do that and then rerun the script.

# Hacking on the MCO - doing builds

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

# Unit Tests

Unit tests (that don't interact with a running cluster) can be executed on a per
package basis with `go test` like so:

`go test -v github.com/openshift/machine-config-operator/pkg/apis`

All tests can be executed with:

`make test`

# Managing Go Dependencies

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

Note this still requires disabling the CVO. It also requires disabling
the operator since the MCD daemonset will be overwritten:

```
oc scale deployment machine-config-operator --replicas=0
```

# How to lay down files with the MCD

1. Create a new machineconfig:


    ```yaml
    # test.yaml
    apiVersion: machineconfiguration.openshift.io/v1
    kind: MachineConfig
    metadata:
      labels:
        machineconfiguration.openshift.io/role: worker
      name: test-file
    spec:
      config:
        storage:
          files:
          - contents:
              source: data:,hello%20world%0A
              verification: {}
            filesystem: root
            mode: 420
            path: /home/core/test
    ```

    Then:

    ```sh
    oc create -f test.yaml
    ```

1. The MCC will then notice this, generate a new merged
   MachineConfig and update the node annotation for the
   worker. You can monitor new MachineConfig objects with:

   ```sh
   oc get machineconfigs --watch
   ```

   You can monitor the MCD logs on a worker to see when it
   reacts to the node annotation change and reboots the
   system:

   ```sh
   oc logs -f machine-config-daemon-<hash>
   ```
