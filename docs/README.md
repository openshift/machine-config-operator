# machine-config-operator

OpenShift 4 is an [operator-focused platform](https://blog.openshift.com/openshift-4-a-noops-platform/),
and the Machine Config operator extends that to the operating system itself,
managing updates and configuration changes to essentially everything between the kernel and kubelet.

To repeat for emphasis, this operator manages updates to systemd, cri-o/kubelet, kernel, NetworkManager,
etc.  It also offers a new `MachineConfig` CRD that can write configuration files onto the host.

The approach here is a "fusion" of code from the original CoreOS
Tectonic as well as some components of Red Hat Enterprise Linux Atomic Host,
as well as some fundamentally new design.

The MCO (for short) interacts closely with
both [the installer](https://github.com/openshift/installer/) as well as Red Hat
CoreOS. See also [the machine-api-operator](https://github.com/openshift/machine-api-operator)
which handles provisioning of new machines - once the machine-api-operator
provisions a machine (with a "pristine" base Red Hat CoreOS), the MCO will take
care of configuring it.

One way to view the MCO is to treat the operating system itself as "just another
Kubernetes component" that you can inspect and manage with `oc`.

The MCO uses [CoreOS Ignition](https://github.com/coreos/ignition) as a configuration
format.  Operating system updates use [rpm-ostree](http://github.com/projectatomic/rpm-ostree), with ostree updates encapsulated inside a container image.  More information in [OSUpgrades.md](OSUpgrades.md).

# Sub-components and design

This one git repository generates 4 components in a cluster; the `machine-config-operator`
pod manages the remaining 3 sub-components.  Here are links to design docs:

 - [machine-config-server](MachineConfigServer.md)
 - [machine-config-controller](MachineConfigController.md)
 - [machine-config-daemon](MachineConfigDaemon.md)

# Interacting with the MCO

Because the MCO is a cluster-level operator, you can inspect its status
just like any other operator that is part of the release image.  If it's reporting success, then that
means that the operating system is up to date and configured.

`oc describe clusteroperator/machine-config`

One level down from the operator CRD, the `machineconfigpool` objects
track updates to a group of nodes.  You will often want to run a command
like this:

`oc describe machineconfigpool`

Particularly note the `Updated` and `Updating` columns.

# Applying configuration changes to the cluster

The MCO has "high level" knobs for some components of the cluster state; for
example, SSH keys and kubelet configuration. However, there are obviously a
quite large number of things one may want to configure on a system. For example,
offline environments may want to specify an internal NTP pool. Another example
is static network configuration. By providing a MachineConfig object
containing [Ignition configuration](https://github.com/coreos/ignition),
systemd units can be provided, arbitrary files can be laid down into writable
locations (i.e. `/etc` and `/var`).

One known ergonomic issue right now for supplying files is that you must encode file contents
via [`data:` URIs](https://en.wikipedia.org/wiki/Data_URI_scheme). This is part of
the current Ignition specification.  The easiest way to encode file contents using this
scheme is via `base64`.  See the example MachineConfig below on how to provide `base64`
encoded file contents

In the example below, the `mode` is in octal (notice the leading `0`); however, decimal is the canonical representation for `mode` when inspecting `MachineConfigs` (in the example, it's `420` below).

This example MachineConfig object replaces `/etc/chrony.conf` with some
custom NTP time servers; see
[the chrony docs](https://chrony.tuxfamily.org/manual.html#Dial_002dup-configuration).

```yaml
# This example MachineConfig replaces /etc/chrony.conf
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 50-examplecorp-chrony
spec:
  config:
    ignition:
      version: 2.2.0
    storage:
      files:
      - contents:
          source: data:text/plain;charset=utf-8;base64,c2VydmVyIGZvby5leGFtcGxlLm5ldCBtYXhkZWxheSAwLjQgb2ZmbGluZQpzZXJ2ZXIgYmFyLmV4YW1wbGUubmV0IG1heGRlbGF5IDAuNCBvZmZsaW5lCnNlcnZlciBiYXouZXhhbXBsZS5uZXQgbWF4ZGVsYXkgMC40IG9mZmxpbmUK
        filesystem: root
        mode: 0644
        path: /etc/chrony.conf
```

```yaml
# oc get machineconfigs -o yaml 50-examplecorp-chrony
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  creationTimestamp: 2019-03-25T18:25:39Z
  generation: 1
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 50-examplecorp-chrony
  resourceVersion: "186713"
  selfLink: /apis/machineconfiguration.openshift.io/v1/machineconfigs/50-examplecorp-chrony
  uid: 6445154f-4f2b-11e9-91e1-021aaf2ce4c0
spec:
  config:
    ignition:
      version: 2.2.0
    storage:
      files:
      - contents:
          source: data:text/plain;charset=utf-8;base64,c2VydmVyIGZvby5leGFtcGxlLm5ldCBtYXhkZWxheSAwLjQgb2ZmbGluZQpzZXJ2ZXIgYmFyLmV4YW1wbGUubmV0IG1heGRlbGF5IDAuNCBvZmZsaW5lCnNlcnZlciBiYXouZXhhbXBsZS5uZXQgbWF4ZGVsYXkgMC40IG9mZmxpbmUK
        filesystem: root
        mode: 420
        path: /etc/chrony.conf
```

The controller will notice the new MachineConfig and generate a new
"rendered" version that looks like `worker-<hash>`.  Use
`oc describe machineconfigpool/worker` to monitor the status of the rollout
of the new rendered config to each node.

Note this configuration only applies to workers (see the `role: worker` label);
currently if you want to apply to both master and workers, you must create two
separate MachineConfig objects.

Practically speaking, one may find it useful to generate your
custom MachineConfig objects from a higher level tool.  Although
in the future ergonomic improvements are planned such as having
a single MC apply to multiple labels, inline file encoding, etc.

# What to look at after creating a MachineConfig

Once you create a MachineConfig fragment like the above, the controller will generate a new "rendered" version that will be used as a target.
For more information, see [MachineConfiguration](MachineConfiguration.md).

In particular, you should look at `oc describe machineconfigpool` and `oc describe clusteroperator/machine-config` as noted above.

# More information about OS updates

The model implemented by the MCO is that the cluster controls the operating system.  OS updates are just another entry in the release image.  For more information, see [OSUpgrades.md](OSUpgrades.md).

# Developing the MCO

See [HACKING.md](HACKING.md).

# Frequently Asked Questions

See [FAQ.md](FAQ.md).

# Security Response

If you've found a security issue that you'd like to disclose confidentially
please contact Red Hat's Product Security team. Details at
https://access.redhat.com/security/team/contact
