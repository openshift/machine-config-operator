# OS updates

The MCO manages the Red Hat Enterprise Linux CoreOS (RHCOS) operating system. Further,
the operating system itself is just another part of the [release image](https://github.com/openshift/cluster-version-operator/), called `machine-os-content`.

In other words, the cluster controls the operating system.

# "Bootimage" vs machine-os-content

We will use the term "bootimage" to mean an initial RHCOS disk image, such
as an AMI, bare metal raw disk image, VMWare VMDK, OpenStack qcow2, etc.  
These bootimages are built using [OSTree](https://github.com/ostreedev/ostree/)
and [rpm-ostree](https://github.com/projectatomic/rpm-ostree/).

Today, [the installer](https://github.com/openshift/installer/) pins the "bootimages"
it uses, and released installers also pin the release image.  As noted above,
release images contain `machine-os-content`, which can be a *different*
RHCOS version.

# The early pivot

We do not want to require that a new bootimage is released for every update,
and in general it can be hard to require that in every environment.  (For
example, bare metal PXE setups)

Hence, the MCO and installer combine to implement what is called "the early pivot".

Let's step through aspects of cluster bootstrap/installation and add some information
about the early pivot.

First, be sure you've read and understood [openshift/installer overview](https://github.com/openshift/installer/blob/37b99d8c9a3878bac7e8a94b6b0113fad6ffb77a/docs/user/overview.md).

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

At this point, Ignition has been executed, and that only runs once. Thereafter,
the MCO takes over for the master nodes. The `machine-config-daemon` daemonset
lands on the master nodes, and will start watching for updates to
the `machine-os-content` from the release image.

A good way to understand this is that now any further config changes or
OS upgrades will go through the `machineconfigpool/master` pool, ensuring
that only 1 machine at a time is changed (via `maxUnavailable: 1` default).

The master machines use the
[machine-api-operator](https://github.com/openshift/machine-api-operator) to
boot the workers.  Each worker pulls Ignition configs from the MCS running
on the control plane, which has the exact same pivot code.  They also
upgrade/reboot, and then join the cluster.

And then finally, the MCD lands on each worker node, same as for the masters.

At this point, the MCO is fully in control of operating system upgrades.
The next time the admin does an `oc adm upgrade`, if a new `machine-os-content`
is provided in the release image, it will be rolled out to the masters
and workers.
