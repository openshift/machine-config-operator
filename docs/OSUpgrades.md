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
RHCOS version.

It's essential to understand that both the bootimage and the `machine-os-content` container
are both essentially wrappers for an [OSTree](https://github.com/ostreedev/ostree) commit.
The OSTree format is an image format designed for in-place operating system updates; it operates
at the filesystem level (like container images) but (unlike container runtimes) has
tooling to manage things such as the bootloader and handling persistence of `/etc` and `/var`.

On top of the OSTree format, Red Hat Enterprise Linux CoreOS uses [pivot](https://github.com/openshift/pivot/)
which is a "glue layer" that handles the encapsulation of the `machine-os-content` (oscontainer)
with the OSTree repository.

# The early pivot

We do not want to require that a new bootimage is released for every update,
and in general it can be hard to require that in every environment (for
example, bare metal PXE setups).  Hence, the MCO and installer combine to
implement "the early pivot".  

By this mechanism the cluster can install using an older bootimage, and
bring the operating system into the state targeted by the `machine-os-content`
in the main release image.  Let's step through aspects of
cluster bootstrap/installation and add some information about the early pivot.

First, be sure you've read and understood [openshift/installer overview](https://github.com/openshift/installer/blob/37b99d8c9a3878bac7e8a94b6b0113fad6ffb77a/docs/user/overview.md).
It's also important to understand the [cluster-version-operator](https://github.com/openshift/cluster-version-operator/) and release image.

In this example we'll use AWS, this process is similar to a situation of
booting bare metal machines via PXE or VMs in OpenStack.

The openshift-installer starts, and uses the AMI it has pinned as the bootstrap node.

The bootstrap node's `bootkube.sh` service pulls the release image, which
contains a reference to the MCO (`machine-config-operator`) and also a
reference to a newer `machine-os-content`. The `bootkube.sh` service runs the MCO in
"bootstrap" mode to generate and serve Ignition to the master machines.

These Ignition configs contain a reference to the newer machine-os-content from
the release image in the `/etc/pivot/image-pullspec` directory.

The master machines boot, pulling their Ignition configs from the bootstrap
node. As part of that `pivot.service` runs (which is
`Before=kubelet.service`, so before the cluster starts). It detects the
reference to the updated `machine-os-content` and pulls it, uses it to upgrade, and
reboots.

The master machines come up and form a cluster.

# Workers and management via the MCO

At this point, Ignition has been executed, and that only runs once.

The master machines use the [machine-api-operator](https://github.com/openshift/machine-api-operator) to
boot the workers.  Each worker pulls Ignition configs from the MCS running
on the control plane, which has the exact same pivot code.  They also
upgrade/reboot, and then join the cluster.

Thereafter, the MCO takes over fully. The `machine-config-daemon` daemonset
lands on the master nodes, and will start watching for updates to
the `machine-os-content` from the release image, as well as any changes
to the `machineconfig` objects.

Every change now will be managed by a `machineconfigpool`, ensuring
that only 1 machine at a time is changed (via `maxUnavailable: 1` default).

However, because of the early pivot, the master nodes have already booted
the desired version (and the Ignition config generated at bootstrap time should
match the in-cluster one) so the MCO has nothing to do.

But now the MCO is fully in control of operating system updates.
The next time the admin does an `oc adm upgrade`, if a new `machine-os-content`
is provided in the release image, it will be rolled out to the masters
and workers.

At the time of this writing, there is not a mechanism to roll out updates
to bootimages.  For example, in EC2, the AMI used will remain the same
for the lifetime of a cluster.  It is likely at some point that the
[machine-api-operator](https://github.com/openshift/machine-api-operator)
will extract bootimage data from the release image, but it is not
yet implemented.
