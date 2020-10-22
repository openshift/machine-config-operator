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

# Understanding /etc/ignition-machine-config-encapsulated.json

See [this pull request](https://github.com/openshift/machine-config-operator/pull/868/commits/ceeb1260215c95c955a2dd3c16b4bc2fe2e6323d).

When a node boots for the first time, it's the main CoreOS [Ignition](github.com/coreos/ignition/)
process which handles the config provided.  This Ignition runs in the initramfs and
performs any repartitioning and filesystem layout, etc.

However, `MachineConfig` objects also have higher level extensions
like `kernelArguments` that *aren't* handled by Ignition.

The invention of `/etc/ignition-machine-config-encapsulated.json` is
a way to bridge these two worlds; it's the target `MachineConfig` object
in JSON form.  The MCS injects this file into the Ignition it serves to the node.
Then when the node boots into e.g. `rendered-worker-0x1234`, the `machine-config-daemon-firstboot.service`
(also written by Ignition) reads that file and handles the bits (kernel arguments, extensions, )
that weren't handled by the "main Ignition" process.

These OS level changes are done along with the OS update process described above.

This ensures that for example if one specifies `nosmt` as a kernel argument,
to turn off hyperthreading to more strongly isolate workloads, that kernel
argument will be applied before `kubelet.service` starts.

# Questions and answers

Q: I upgraded OpenShift and noticed that my AMI hasn't changed, is this normal?

Yes, see: https://github.com/openshift/enhancements/pull/201
(As well as the rest of this document - we do in-place updates without changing the bootimage)

Q: Is the integrity of operating system upgrades verified?

The overall approach here is that the operating system is just one part of the cluster.
Integrity of the OpenShift platform is handled to start by the
[cluster version operator](https://github.com/openshift/cluster-version-operator).
Today the CVO will by default GPG verify the integrity of the release image
before applying it.  The release image contains a `sha256` digest of `machine-os-content`
which is used by the MCO for updates.  On the host, the container runtime
`podman` verifies the integrity of that `sha256` when pulling the image,
before the MCO reads its content.  Hence, there is end-to-end GPG-verified integrity
for the operating system updates (as well as the rest of the cluster components
which run as regular containers).

Q: Why do you do this weird "ostree repository in container" thing?  Why ostree?

We're using a system that works; ostree is a well tested "image like" update
system that has been in use for many years by multiple distributions.  It handles
SELinux and bootloaders, etc.  We're just "encapsulating" that system inside
a container image for all of the above reasons (management, etc.).

At some point in the future though it's likely that we will try to change
the `machine-os-content` container to look more like an unpacked container image.

Q: How do I look at the content in the ostree repository inside the container?

You can get the `ostree` tool from many distributions; try e.g.
`yum -y install ostree` inside a [RHEL UBI](https://www.redhat.com/en/blog/introducing-red-hat-universal-base-image)
container for example.

From there, probably the simplest thing is to use `oc image extract`
to unpack the container image.  Something like this:
```
$ mkdir machine-os-content
$ oc image extract quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:02d810d3eb284e684bd20d342af3a800e955cccf0bb55e23ee0b434956221bdd --path /:machine-os-content
$ find machine-os-content/srv/repo/ -name '*.commit'
machine-os-content/srv/repo/objects/33/dd81479490fbb61a58af8525a71934e7545b9ed72d846d3e32a3f33f6fac9d.commit
$ ostree --repo=machine-os-content/srv/repo ls 33dd81479490fbb61a58af8525a71934e7545b9ed72d846d3e32a3f33f6fac9d
d00755 0 0      0 /
l00777 0 0      0 /bin -> usr/bin
l00777 0 0      0 /home -> var/home
l00777 0 0      0 /lib -> usr/lib
l00777 0 0      0 /lib64 -> usr/lib64
l00777 0 0      0 /media -> run/media
l00777 0 0      0 /mnt -> var/mnt
l00777 0 0      0 /opt -> var/opt
l00777 0 0      0 /ostree -> sysroot/ostree
l00777 0 0      0 /root -> var/roothome
l00777 0 0      0 /sbin -> usr/sbin
l00777 0 0      0 /srv -> var/srv
d00755 0 0      0 /boot
d00755 0 0      0 /dev
d00755 0 0      0 /proc
d00755 0 0      0 /run
d00755 0 0      0 /sys
d00755 0 0      0 /sysroot
d01777 0 0      0 /tmp
d00755 0 0      0 /usr
d00755 0 0      0 /var
$
```
