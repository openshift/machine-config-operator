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

(More to come)

# Developing the MCO

See [HACKING.md](HACKING.md).

