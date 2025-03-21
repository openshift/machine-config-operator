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
format.  Operating system updates use [rpm-ostree](http://github.com/projectatomic/rpm-ostree), with ostree updates encapsulated inside a container image.  More information in [OSUpgrades.md](/docs/OSUpgrades.md).

As of release 4.12, you can try out [OCP CoreOS Layering](/docs/UsingLayering.md) which lets you use more familiar "Containerfile" (Dockerfile) syntax to apply configuration to your pools.

# Sub-components and design

This one git repository generates 4 components in a cluster; the `machine-config-operator`
pod manages the remaining 3 sub-components.  Here are links to design docs:

 - [machine-config-server](/docs/MachineConfigServer.md)
 - [machine-config-controller](/docs/MachineConfigController.md)
 - [machine-config-daemon](/docs/MachineConfigDaemon.md)

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

See the [OCP product documentation](https://docs.openshift.com/container-platform/4.10/post_installation_configuration/machine-configuration-tasks.html)
for more information.

# What to look at after creating a MachineConfig

Once you create a MachineConfig fragment like the above, the controller will generate a new "rendered" version that will be used as a target.
For more information, see [MachineConfig](/docs/MachineConfig.md).

In particular, you should look at `oc describe machineconfigpool` and `oc describe clusteroperator/machine-config` as noted above.

# More information about OS updates

The model implemented by the MCO is that the cluster controls the operating system.  OS updates are just another entry in the release image.  For more information, see [OSUpgrades.md](/docs/OSUpgrades.md).

# Developing the MCO

See [HACKING.md](/docs/HACKING.md).

# Frequently Asked Questions

See [FAQ.md](/docs/FAQ.md).

# Security Response

If you've found a security issue that you'd like to disclose confidentially
please contact Red Hat's Product Security team. Details at
https://access.redhat.com/security/team/contact
