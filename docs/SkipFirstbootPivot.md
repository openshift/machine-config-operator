# Skip Firstboot Pivot

This document describes the skip-firstboot-pivot feature. The feature lets
joining RHCOS nodes apply the target OS image live during first boot instead
of rebooting into it. The node runs with up-to-date binaries from the target
image immediately. The actual deployment switch happens on the next cluster
upgrade.

## Problem

A new RHCOS node that joins an OpenShift cluster usually has an older boot
image than the cluster's current OS image. The MCD detects the mismatch,
rebases to the target image, and reboots. This reboot adds several minutes
to node provisioning.

## Solution

The feature uses `rpm-ostree apply-live --allow-replacement` to overlay the
target image's content onto the running system without rebooting. The node
boots once, applies the overlay, and joins the cluster. The staged deployment
takes effect on the next reboot (typically the next cluster upgrade).

## Boot sequence

A systemd generator replaces the default boot target with a minimal target.
That target runs the overlay chain before it hands off to multi-user.target.

```
generator
  └─ firstboot-pivot-skip.target
       └─ pull service (rebase + apply-live)
            └─ finalize service (start OVS, restart NM, create marker, isolate)
                 └─ multi-user.target
                      └─ MCD firstboot (writes MC files, applies kargs, skips reboot)
                           └─ kubelet + crio start
```

## Template files

### files/firstboot-pivot-skip-flag.yaml

A static flag file at `/etc/machine-config-daemon/skip-firstboot-pivot`. Its
presence tells the generator to activate the pivot-skip flow. When the feature
gate is wired up, Ignition writes this file conditionally. For now it is
always present.

### files/firstboot-pivot-skip-generator.yaml

A systemd generator at
`/etc/systemd/system-generators/firstboot-pivot-skip-generator`.
It runs early in boot and checks for two files:

- The flag file (`/etc/machine-config-daemon/skip-firstboot-pivot`)
- The firstboot sentinel (`/etc/ignition-machine-config-encapsulated.json`)

If both exist, the generator sets the default boot target to
`firstboot-pivot-skip.target`. It also creates a wants link so that
`firstboot-osupdate.target` runs after the isolation to multi-user.target.

### files/firstboot-pivot-skip-pull.sh.yaml

The main script at `/usr/local/bin/firstboot-pivot-skip-pull.sh`. It does the
following, in order:

1. Extracts the target OS image URL from the encapsulated MachineConfig.
2. If `/var/run/NetworkManager/resolv.conf` exists, copies it to
   `/etc/resolv.conf`. Some platforms configure NetworkManager with
   `rc-manager=unmanaged`, which means NM does not write `/etc/resolv.conf`.
   Other services that populate `/etc/resolv.conf` only run under
   `multi-user.target`. This copy gives the pull service DNS resolution
   under the minimal target.
3. Saves `/usr/lib/modules` to `/run/modules-save`. The target image has a
   different kernel version. Without this save, the running kernel's modules
   disappear after apply-live.
4. Runs `rpm-ostree rebase` to stage the target deployment. The rebase runs
   in a retry loop with a 120-second timeout. The external network interface
   can take time to become routable after boot. If the timeout expires, the
   script exits with an error and the fallback service takes over.
5. Runs `rpm-ostree apply-live --allow-replacement` to overlay the target
   image's `/usr` onto the running system.
6. Bind-mounts the saved kernel modules back over `/usr/lib/modules`.
7. Runs `semodule -R` to reload SELinux policy from the target image.

### units/firstboot-pivot-skip-pull.service.yaml

The systemd unit that runs the pull script. It uses `Wants=network.target`
and `After=network.target NetworkManager.service`. This ordering makes
networking available for the image pull. Because it uses `Wants=` instead of
`Requires=`, a NetworkManager restart does not cascade-kill this service.

The service has `OnFailure=firstboot-pivot-skip-fallback.service`. If the
pull script fails, the fallback service runs and isolates to
`multi-user.target`. The node then follows the normal reboot path.

### units/firstboot-pivot-skip-finalize.service.yaml

Runs after the pull service completes. It does the following:

1. `systemctl daemon-reload` to pick up new unit files from apply-live.
2. `systemctl start openvswitch` because the openvswitch package was added by
   apply-live but its service was not running.
3. `systemctl restart NetworkManager` to load the NetworkManager-ovs plugin
   that apply-live added. This runs here (not in the pull service) because
   the pull service has an ordering dependency on NetworkManager.
4. Creates the marker file at `/etc/machine-config-daemon/firstboot-pivot-skipped`.
5. Isolates to `multi-user.target` to continue the normal boot sequence.

Uses `KillMode=none` so the isolate command does not get interrupted.

The finalize service also has `OnFailure=firstboot-pivot-skip-fallback.service`
as a safety net.

### units/firstboot-pivot-skip-fallback.service.yaml

A oneshot service that runs `systemctl --no-block isolate multi-user.target`.
Both the pull service and the finalize service declare it as their
`OnFailure=` target. If either service fails, the fallback isolates to
`multi-user.target` and the node continues through the normal reboot path.

### units/firstboot-pivot-skip.target.yaml

The boot target that the generator sets as default. It requires
`basic.target`, `sshd.service`, `getty.target`, `systemd-user-sessions.service`,
and `firstboot-pivot-skip-finalize.service`. This gives the node basic
networking and console access while the overlay chain runs.

## Modified upstream service

### units/machine-config-daemon-firstboot.service.yaml

Two additions:

- `After=firstboot-pivot-skip-finalize.service` so the MCD waits for the
  overlay chain to complete before it starts.
- `ExecStartPost=-/usr/sbin/sysctl --system` to load the sysctl values
  that the MCD wrote to `/etc/sysctl.d/`. Normally these values take effect
  on reboot. Because we skip the reboot, they must be loaded explicitly. In
  the normal (non-pivot-skip) path, the reboot kills the process before this
  line runs.

## Limitations

### apply-live and major OS version differences

`rpm-ostree apply-live` cannot overlay directory structure changes between
the boot image and the target image. It fails with
`error: Changed directories are not supported yet`. Different OS generations
trigger this error (for example, RHCOS 9.x to RHCOS 10.x).

When the boot image and the target image are from the same OS generation,
apply-live works. The fallback service handles the failure case: the node
reboots through the normal path and joins the cluster without degradation.

### Kernel mismatch

The overlaid node runs the boot image's kernel with the target image's
userspace. The staged deployment (with the target kernel) takes effect on
the next reboot. Kernel modules from the boot image are bind-mounted back
into place after apply-live so the running kernel can load them.
