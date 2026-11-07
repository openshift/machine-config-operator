# ADR-001: On-Cluster Layering (OCL)

## Status
Implemented (TechPreview)

## Context
Traditional MCO updates apply Ignition configs and use rpm-ostree to manage the OS. Users need to add custom packages or content to RHCOS nodes but have no supported mechanism to build custom OS images integrated with the MCO update lifecycle.

## Decision
Introduce On-Cluster Layering: users create a `MachineOSConfig` with a Containerfile, MCO builds a custom OS image on-cluster via `MachineOSBuild` Jobs (podman build), and the resulting image is applied to nodes in the target pool.

## Components
- `MachineOSConfig` CRD: build inputs (Containerfile, pool ref)
- `MachineOSBuild` CRD: build execution tracking
- `machine-os-builder` binary: orchestrates builds
- Build Controller (`pkg/controller/build/`): reconciles MachineOSBuild lifecycle
- Image Builder (`pkg/controller/build/imagebuilder/`): podman/buildah job creation
- Image Pruner (`pkg/controller/build/imagepruner/`): cleanup of old images

## Consequences
- Node Controller must coordinate with build status before rolling updates
- MachineConfigPool gains build-related conditions (BuildPending, Building, BuildSuccess, BuildFailed)
- Daemon must support bootc-based updates in addition to rpm-ostree
- Feature-gated CRD variants needed for gradual rollout
