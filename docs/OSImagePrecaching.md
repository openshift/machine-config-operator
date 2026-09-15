# RHCOS OS image pre-caching

This document describes background pre-caching of RHCOS (rpm-ostree) OS images
during cluster upgrades.

Related bug: [OCPBUGS-87861](https://issues.redhat.com/browse/OCPBUGS-87861)

## Problem

During a cluster upgrade the Machine Config Daemon (MCD) rebases rpm-ostree to
the `rhel-coreos` image referenced by the rendered `MachineConfig` `OSImageURL`.
Today that download typically happens **after** the node is cordoned and drained.

On slow or bandwidth-constrained links each hop can add ~10–20 minutes of
per-node downtime. Multi-hop upgrades (for example
4.18.z → 4.19.z → 4.20.z → 4.21.z → 4.22.z) compound this delay.

Container image pre-pull mechanisms such as `PinnedImageSet` populate CRI-O
storage only. They do **not** populate the host ostree repository and therefore
do not reduce rpm-ostree rebase time.

## Approach

rpm-ostree supports offline staging:

| Phase | Command | When |
|-------|---------|------|
| Pre-cache | `rpm-ostree rebase <ref> --download-only` | Background, node still schedulable |
| Apply | `rpm-ostree rebase <ref> --cache-only` | During OS update, before network pull fallback |

The MCD:

1. Periodically checks whether the node `desiredImage` annotation differs from
   the booted OS image and the node is not draining.
2. Runs `--download-only` for that image while the node remains schedulable.
3. During `updateLayeredOS`, attempts `--cache-only` before falling back to the
   existing online registry rebase path.

If pre-cache fails or is incomplete, behavior matches the previous online-only
flow.

## Observability

Look for log lines from the MCD pod:

```
OS image precache: downloading layers for ...
OS image precache completed for ...
Updated OS to ... from local ostree cache
Cached OS rebase unavailable for ..., pulling from registry
```

## Future work

- `PinnedOSImageSet` CR for explicit multi-hop target digests
- Automatic pre-cache of intermediate CVO hop images
- `MachineConfigNode` status reporting (mirroring `PinnedImageSet`)
- Feature gate for TechPreview rollout
- User-facing OpenShift documentation

## References

- [OSUpgrades.md](OSUpgrades.md)
- [rpm-ostree administrator handbook](https://coreos.github.io/rpm-ostree/administrator-handbook/)
- [PinnedImageSet (CRI-O only)](https://docs.redhat.com/en/documentation/openshift_container_platform/latest/html/machine_configuration/machine-config-pin-preload-images-about)
