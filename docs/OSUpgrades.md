# OS updates

The MCO manages the Red Hat Enterprise Linux CoreOS (RHCOS) operating system. Further,
the operating system itself is just another part of the [release image](https://github.com/openshift/cluster-version-operator/), called `machine-os-content`.

In other words, the cluster controls the operating system.

# "Bootimage" vs machine-os-content

We will use the term "bootimage" to mean an initial RHCOS disk image, such
as an AMI, bare metal raw disk image, VMWare VMDK, OpenStack qcow2, etc.  
These bootimages are built using [coreos-assembler](https://github.com/coreos/coreos-assembler).

Today, [the installer](https://github.com/openshift/installer/) pins the "bootimages"
it uses, and released installers also pin the release image.  As noted above,
release images contain `machine-os-content`, which can be a *different*
RHCOS version.  You can find the installer-pinned bootimage in e.g. [this file](https://github.com/openshift/installer/blob/release-4.4/data/data/rhcos.json).

[A pending enhancement](https://github.com/openshift/enhancements/pull/201) describes
generating and inspecting bootimage data from the release image
(not yet implemented).

It's essential to understand that both the bootimage and the `machine-os-content` container
are both essentially wrappers for an [OSTree](https://github.com/ostreedev/ostree) commit.
The OSTree format is an image format designed for in-place operating system updates; it operates
at the filesystem level (like container images) but (unlike container runtimes) has
tooling to manage things such as the bootloader and handling persistence of `/etc` and `/var`.

The reason we wrap an OSTree commit inside a container image is so that
the release image encapsulates basically everything about a cluster (except the bootimage).
This makes it easy to mirror updates offline.

# Applying OS updates before kubelet

We do not want to require that a new bootimage is released for every update,
and in general it can be hard to require that in every environment (for
example, bare metal PXE setups).

[As of today](https://github.com/openshift/machine-config-operator/pull/1766/), when a node boots the MCO serves it Ignition for configuration,
including a systemd unit called `machine-config-operator-firstboot.service`
which pulls code onto the host, and then it runs `Before=kubelet.service`
to perform an OS update and reboot.

One important property of this is that it means OS updates are applied
before any potentially untrusted workloads land on the node.  Because we just
use `podman`, we also don't have to worry about having old `kubelet` versions
join a cluster.

# Understanding OS updates at installation time

First, be sure you've read and understood [openshift/installer overview](https://github.com/openshift/installer/blob/37b99d8c9a3878bac7e8a94b6b0113fad6ffb77a/docs/user/overview.md).
It's also important to understand the [cluster-version-operator](https://github.com/openshift/cluster-version-operator/) and release image.

In this example we'll discuss AWS, but this process is similar to a situation of
booting bare metal machines via PXE, or Azure, or a private OpenStack instance.

The openshift-installer starts, and uses the AMI it has pinned as the bootstrap node
as well as for the control plane.

The bootstrap node's `bootkube.sh` service pulls the release image, which
contains a reference to the MCO (`machine-config-operator`) and also a
reference to a newer `machine-os-content`. The `bootkube.sh` service runs the MCO in
"bootstrap" mode to generate and serve Ignition to the master machines.

The control plane nodes wait in the initramfs, retrying until they are able to
fetch the Ignition config from the bootstrap node.

When that succeeds, the above process of `machine-config-daemon-firstboot.service`
runs which extracts OS updates from the `machine-os-content` container image,
and the control plane nodes each reboot (before `kubelet.service` has started).

When the control plane nodes reboot and form a cluster, the bootstrap
node is torn down.

# Workers

At this point, Ignition has been executed, and that only runs once.
The `machine-config-daemon-firstboot.service` is no longer used for OS updates.

The master machines use the [machine-api-operator](https://github.com/openshift/machine-api-operator) to
boot the workers.  Each worker pulls Ignition configs from the MCS running
on the control plane.  The exact same process of performing an upgrade
and reboot happens for each worker.

# Management via the Machine Config Daemon

After a node (whether control plane or worker) has joined the cluster, the MCO
takes over.  Previously, each individual node was running systemd units;
now changes are coordinated via the MCO.

When the administrator starts an `oc adm upgrade`, if a new `machine-os-content`
is provided in the release image, it will be rolled out to the control plane
and workers.

Every change now will be managed by a `machineconfigpool`, ensuring
that only 1 machine at a time is changed (via `maxUnavailable: 1` default).

# MCD host upgrade execution

Today mostly because of [SELinux reasons](https://bugzilla.redhat.com/show_bug.cgi?id=1839065) the
MCD copies itself to the host, and runs as part of the host context.
Then it provides updated content to `rpm-ostreed.service`, which is already
a daemon running on the host.

The MCD tries to watch the systemd journal for relevant service and proxy logs,
so you *should* be able to `oc -n openshift-machine-config-operator logs -c machine-config-daemon pod/machine-config-daemon-...`
to debug.

# Updating bootimages

See: https://github.com/openshift/enhancements/pull/201
