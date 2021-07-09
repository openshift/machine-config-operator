# MachineConfigDaemon

## Goals

1. Apply new machine configuration during update.

2. Validate and verify machine's state to the requested machine configuration.

## Non Goals

1. MachineConfigDaemon does not execute scripts on the machines.

## Overview

MachineConfigDaemon is scheduled on the machines in a cluster as a DaemonSet. This daemon is responsible for performing machine updates in OpenShift 4. The update will include tasks related to the systemd units, files on disk, operating system upgrades etc. The MachineConfigDaemon updates a machine to configuration defined by MachineConfig as instructed by the MachineConfigController.

The MachineConfigDaemon is also responsible for annotating a node with `machineconfiguration.openshift.io/ssh=accessed` when it detects an SSH access to the machine.

## Supported vs Unsupported Ignition config changes

The MachineConfigDaemon receives machine configuration in the form of a "rendered" or merged MachineConfig which is generated from applicable fragments by the controller.

If the updated Ignition config contains changes compatible with the current config, the node will be updated in place.  Otherwise, it will enter a "degraded" state; the idea is that a human or automation tooling can then re-provision degraded machines.

Not all Ignition config sections are supported; see the following table:

Ignition spec 2 sections | Supported
--- | ---
Files | YES
systemd Units | YES
Networkd | NO
Users | NO *
Directories | NO
FileSystems | NO
Links | NO
Disks | NO
RAID | NO

Ignition spec 3 sections | Supported
--- | ---
Files | YES
systemd Units | YES
Users | NO *
Groups | NO
Directories | NO
FileSystems | NO
Links | NO
Disks | NO
RAID | NO

\* At this time only updates to `sshAuthorizedKeys` for user `core` are permitted. Please see [Update-SSHKeys](./Update-SSHKeys.md) for details.

## Coordinating updates

The MachineConfigDaemon uses [annotations defined](./MachineConfigController.md#updatecontroller-interface-with-machineconfigdaemon) on the Node object to coordinate updates with MachineConfigController for the machine.

![MachineConfigDaemon update flow](./MachineConfigDaemonUpdate.svg)

### States

1. `Done` when daemon sets currentConfig = desiredConfig

2. `Working` when daemon starts updating the machine.

3. `Degraded` when daemon cannot continue to apply the update.

## OS updates

In addition to handling Ignition configs, the MachineConfigDaemon also takes
care of updating the base operating system.

Updates are provided via the `OSImageURL` component of a MachineConfig object.
This should generally be controlled by the
[cluster-version-operator](https://github.com/openshift/cluster-version-operator/),
and its current existence in MachineConfig objects should be thought of as an
implementation detail.

MachineConfigDaemon only supports updating Red Hat CoreOS, which uses rpm-ostree.
The `OSImageURL` refers to a container image that carries inside it an OSTree payload.  When
the `OSImageURL` changes, it will be passed to the [pivot](https://github.com/openshift/pivot)
command which is included in Red Hat CoreOS, and in turn takes care of passing it
to rpm-ostree.

Once an update is prepared (in terms of a new bootloader entry which points to a
new OSTree "deployment" or filesystem tree), then the MachineConfigDaemon will
reboot.

### Verification

Upon start, MachineConfigDaemon queries rpm-ostree to determine the booted system version
and verifies it matches the expected config.

## systemd unit updates

MachineConfigDaemon replaces the unit service files on disk. The updated systemd services run after machine reboot.

The daemon should prune all the systemd units that don't exist in the desiredConfig but existed before. Diff the current config and desired config, then remove the units that were removed.

### Verification

1. MachineConfigDaemon verifies that contents and existence of the systemd unit files.

2. MachineConfigDaemon also verifies that the systemd service is enabled when specified in Ignition config.

## Directory / File updates

MachineConfigDaemon replaces the file contents on disk with the contents of the file from the desiredConfig.

The daemon should apply any change in permissions on file / directories.

The daemon should prune all the files and directories that don't exist in the desiredConfig but existed before. Diff the current config and desired config, then remove the nodes that were removed.

### Verification

When starting, MachineConfigDaemon verifies that contents and existence of the files and directories match the current configuration.  If the MachineConfigDaemon is coming up after applying a "pending" configuration, it will become current, and then verification will proceed.

## Machine reboot

With the exception of [optimized updates](#optimized-updates), the MachineConfigDaemon will drain and reboot the machine after applying the updated machine configuration.

## Node drain

The daemon performs a best-effort node drain before rebooting.

The node drain behavior:

1. Should not try to remove static pods.

2. Should respect pod disruption policy for evictions.

3. Should not evict pods marked with critical annotation.

4. Should not evict itself from the node.

### Node drain on master nodes

The draining on master nodes should not be different from worker node as the control plane is self-hosted.

### Node drain master in single master

The draining of pods on the only master node will not evict the control plane as they have critical pod annotation. After rebooting the only master, the pod-checkpointer brings back the components responsible for restarting the control plane.

### Node drain etcd static pods on masters

Etcd is co-located on master nodes as static pods. The draining behavior defined above prevents draining of static pods to prevent interference to etcd cluster by the daemon.

## Optimized Updates

As of Openshift 4.7, the MCD gained the functionality to apply select MachineConfig updates without a full reboot flow (drain -> update -> reboot). The action is calculated as a diff between current and desired configurations. For any MachineConfig change not listed below, or if a forcefile was set, the MCD will trigger the full reboot flow.

The updated list of optimized updates and behaviour (as of Openshift 4.8) is as follows:

### Drainless and Rebootless Updates

"None" action: only performs the corresponding file write. The following changes will not trigger a drain nor a reboot:

1. [SSH Keys](./Update-SSHKeys.md) (updating ignition/passwd/users/sshAuthorizedKeys section in a MachineConfig)
2. kube-apiserver-to-kubelet-signer CA cert (located at `/etc/kubernetes/kubelet-ca.crt`, 1 year expiry autorotated by the openshift-kubeapiserver operator)
3. [Pull Secret](./PullSecret.md) (cluster-wide, located at `/var/lib/kubelet/config.json`).
4. **Selected registries.conf changes(/etc/containers/registries.conf)**  This file is generally changed via ICSP object changes. Only the following changes will cause no-drain and no-reboot updates:
   - addition of a registry with mirror-by-digest-only=true
   - addition of a mirror in a registry with mirror-by-digest-only=true
   - appending items in unqualified-search-registries list

### Rebootless Updates

"Crio Reload" action: performs the file write, and runs a `systemctl reload crio`. The following changes will trigger a drain, but not a reboot:

1. **registries.conf (/etc/containers/registries.conf)**: This file is generally changed via ICSP object changes. Node drain will take place except for changes specified in [Drainless and Rebootless Updates](#Drainless-and-Rebootless-Updates).

## Annotating on SSH access

RHCOS nodes in Openshift are not meant to be manually accessed via SSH. MCD uses logind to watch for login sessions, which, upon detection, warns the user and annotates the node with `machineconfiguration.openshift.io/ssh=accessed`. This in turn will be used to warn cluster admins.