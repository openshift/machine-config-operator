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
format.  Operating system updates use [rpm-ostree](http://github.com/projectatomic/rpm-ostree), with ostree updates encapsulated inside a container image.  More information in [OSUpgrades.md](docs/OSUpgrades.md).

For more, see [the documentation](docs/README.md).

# Developing the MCO

See [HACKING.md](docs/HACKING.md).

# Frequently Asked Questions

See [FAQ.md](docs/FAQ.md).

# Security Response

If you've found a security issue that you'd like to disclose confidentially
please contact Red Hat's Product Security team. Details at
https://access.redhat.com/security/team/contact
