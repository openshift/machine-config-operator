# machine-config-operator

This operator is an integral part of the operator-focused OpenShift 4 platform.
It manages and applies configuration and updates of the base operating system
and container runtime; essentially everything between the kernel and kubelet.

The approach here is a "fusion" of code from the original CoreOS
Tectonic as well as some components of Red Hat Enterprise Linux Atomic Host,
as well as some fundamentally new design.

The MCO (for short) interacts closely with
both [the installer](https://github.com/openshift/installer/) as well as Red Hat
CoreOS. See also [the machine-api-operator](https://github.com/openshift/machine-api-operator)
which handles provisioning of new machines - once the machine-api-operator
provisions a machine (with a "pristine" base Red hat CoreOS), the MCO will take
care of configuring it.

One way to view the MCO is to treat the operating system itself as "just another
Kubernetes component" that you can inspect and manage with `oc`.

The MCO uses [CoreOS Ignition](https://github.com/coreos/ignition) as a configuration
format.  Operating system updates use [rpm-ostree](http://github.com/projectatomic/rpm-ostree).

# Sub-components and design

This operator is split into 4 components; the above covers
the operator.  Here are links to design docs for the sub-components:

 - [machine-config-server](docs/MachineConfigServer.md)
 - [machine-config-controller](docs/MachineConfigController.md)
 - [machine-config-daemon](docs/MachineConfigDaemon.md)

# Interacting with the MCO

View operator status:
`oc describe clusteroperator/machine-config-operator`

Inspect the status of the `machineconfigpool` objects which track upgrades:
`oc describe machineconfigpool`

# Applying configuration changes to the cluster

The MCO has "high level" knobs for some components of the cluster state; for
example, SSH keys and kubelet configuration. However, there are obviously a
quite large number of things one may want to configure on a system. For example,
offline environments may want to specify an internal NTP pool. Another example
is static network configuration. By providing a MachineConfig object
containing [Ignition configuration](https://github.com/coreos/ignition),
systemd units can be provided, arbitrary files can be laid down into `/etc` and `/var`, etc.

One known ergonomic issue right now for supplying files is that you must encode file contents
via [`data:` URIs](https://en.wikipedia.org/wiki/Data_URI_scheme). This is part of
the current Ignition specification.

Note also the `mode` is in decimal; mode `420` is octal `0644` or `rw-r--r--`.

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
    storage:
      files:
      - contents:
          source: data:,server%20foo.example.net%20maxdelay%200.4%20offline%0Aserver%20bar.example.net%20maxdelay%200.4%20offline%0Aserver%20baz.example.net%20maxdelay%200.4%20offline
          verification: {}
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

# Developing the MCO

See [HACKING.md](HACKING.md).
