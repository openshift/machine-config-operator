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

With the exception of [rebootless updates](#rebootless-updates), the MachineConfigDaemon will drain and reboot the machine after applying the updated machine configuration.

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

## Rebootless Updates

As of Openshift 4.7, the MCD gained the functionality to apply select MachineConfig updates without a full reboot flow (drain -> update -> reboot). The MCD now calculates a diff between the current and desired configurations, and it uses any changes to select one of the options listed below. For any change not listed below, or if a forcefile was set, the MCD will trigger the full reboot flow.

The updated list of optimized updates and behaviour (as of Openshift 4.10) is as follows:

### Without Drain

Most reboot exceptions also skip a drain, but some have to reload crio.

#### "None" Action

The "None" action only performs the corresponding file write; it does not trigger a drain or a reboot. This action is taken for changes to the following items:

1. [SSH Keys](./Update-SSHKeys.md): updated by changing `ignition.passwd.users.sshAuthorizedKeys` in a MachineConfig
2. kube-apiserver-to-kubelet-signer CA cert: located at `/etc/kubernetes/kubelet-ca.crt` and autorotated by the openshift-kube-apiserver operator after a 1 year expiry
3. [Pull Secret](./PullSecret.md): cluster-wide, located at `/var/lib/kubelet/config.json`

#### "Reload Crio" Action

The "Reload Crio" action performs the file write and runs a `systemctl reload crio`. It does not trigger a drain or a reboot for changes to the following items:

1. Container signing GPG keys: these can be changed by pointing `/etc/containers/policy.json` to `/etc/machine-config-daemon/no-reboot/containers-gpg.pub` and storing keys in the latter file. Changes to either file trigger the "Reload Crio" action
2. **Selected** `/etc/containers/registries.conf` changes: this file is generally changed via ICSP object changes. Only the following changes will avoid a drain:
   - addition of a registry with `pull-from-mirror=digest-only` for each mirror
   - addition of a mirror with `pull-from-mirror=digest-only` in a registry
   - appending items in the `unqualified-search-registries` list

### With Drain

"Reload Crio" is performed with a drain for changes to the following items:

1. **Selected** `/etc/containers/registries.conf` changes: this file is generally changed via ICSP object changes. Node drain will take place except for changes specified [above](#Without-Drain).

## Config Drift Detection

### Overview

There may be situations when the on-disk state of a node may differ or "drift"
from what is specified by the MachineConfig. This is usually (though not
always) due to a cluster admin manually editing files. In the past, the MCD
would only verify that the on-disk state matches what is specified by the
MachineConfig after a reboot. Whenever this occurs, the node and Machine Config
Pool (MCP) would be marked Degraded and unable to apply any MachineConfigs.

Because reboots do not frequently occur, the config drift isn't immediately
detected. In cases where the config drift was intentional, the cluster admin
may lose the context which necessitated the config drift between the time the
drift occurred and when it was detected. This leads to the node and / or MCP
becoming degraded at an inconvenient time and can block future updates until
the issue is remedied.

### Detection

To avoid this, a Config Drift Monitor was built into the MCD in OCP 4.10. It
uses `fsnotify` to proactively detect config drift and mark the node `Degraded`
within seconds of the drift taking place.

Whenever a filesystem write event is detected for any of the objects (Ignition
files and systemd units / dropins) defined in the currently applied
MachineConfig, the Config Drift Monitor validates that the file contents and
permissions fully match what the currently-applied MachineConfig specifies.

Whenever the Config Drift Monitor detects an inconsistent object, it will:
1. Emit an error to the console logs.
1. Emit a Kubernetes event indicating that a configuration drift has occurred.
1. Stop further verification.
1. Set `machineconfiguration.openshift.io/state` to `Degraded`. 

### Machine Config Updates

Prior to applying a new MachineConfig, a preflight check is made to verify that
the current on-disk state matches the active MachineConfig. If config drift is
detected, `machineconfiguration.openshift.io/state` will be set to `Degraded`
and the update will not be applied until recovery steps are taken.

To apply a new MachineConfig, the Config Drift Monitor is temporarily shut down.
This is because the config will "drift" from the current MachineConfig to the
new MachineConfig. Once new MachineConfig has been applied (assuming no reboot
is needed), the Config Drift Monitor will be restarted and supplied with the
newly active MachineConfig. If a reboot is required, the Config Drift Monitor
will be started once the MCD is out of the booting phase.

Whenever the Config Drift Monitor is started or stopped, a Kubernetes Event
will be emitted. At startup, the event will also include the name of the
MachineConfig it is using as a reference.

### Recovering From Config Drift

Once config drift is detected, there are two options for recovery:

1. Ensure that the contents and file permissons of the file on disk match what
the MachineConfig specifies. This can be done by manually rewriting the file
contents or changing the file mode. Once this is done, the MCD will reapply the
current MachineConfig.
1. Create the forcefile (`/run/machine-config-daemon-force`). This will cause
the MCD to bypass the preflight config checks and reapply the current
MachineConfig. This will also cause the node to reboot, which may not be
desirable.
