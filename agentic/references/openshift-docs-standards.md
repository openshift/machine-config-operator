# OpenShift Documentation Standards

> **Purpose**: Standard documentation links for OpenShift and related technologies

## Official Documentation

**Product Docs**: https://docs.openshift.com/container-platform/latest/

**Relevant Sections**:
- [Machine Configuration](https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html)
- [Node Management](https://docs.openshift.com/container-platform/latest/nodes/index.html)
- [Operator Development](https://docs.openshift.com/container-platform/latest/operators/index.html)

**API Reference**: https://docs.openshift.com/container-platform/latest/rest_api/

**Relevant API Groups**:
- [MachineConfiguration APIs](https://docs.openshift.com/container-platform/latest/rest_api/machine_apis/machineconfig-machineconfiguration-openshift-io-v1.html)
- [Config APIs](https://docs.openshift.com/container-platform/latest/rest_api/config_apis/)

## OpenShift Projects

**Enhancements**: https://github.com/openshift/enhancements

**API Definitions**: https://github.com/openshift/api

**Operator Patterns**: https://github.com/openshift/library-go

**Installer**: https://github.com/openshift/installer

## Upstream Kubernetes

**Kubernetes Docs**: https://kubernetes.io/docs/

**Relevant Sections**:
- [Nodes](https://kubernetes.io/docs/concepts/architecture/nodes/)
- [DaemonSet](https://kubernetes.io/docs/concepts/workloads/controllers/daemonset/)
- [Custom Resources](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)

**KEPs (Kubernetes Enhancement Proposals)**: https://github.com/kubernetes/enhancements

## CoreOS / RHCOS

**Red Hat CoreOS**: https://docs.openshift.com/container-platform/latest/architecture/architecture-rhcos.html

**Ignition**: https://coreos.github.io/ignition/

**rpm-ostree**: https://github.com/coreos/rpm-ostree

**OSTree**: https://ostreedev.github.io/ostree/

## Container Runtime

**CRI-O**: https://cri-o.io/

**Podman**: https://podman.io/

## Go Libraries

**controller-runtime**: https://pkg.go.dev/sigs.k8s.io/controller-runtime

**client-go**: https://pkg.go.dev/k8s.io/client-go

**library-go**: https://github.com/openshift/library-go

## How to Use These Links

**When implementing features**:
- Check OpenShift product docs for user-facing behavior
- Check API reference for field definitions
- Check enhancements for design rationale
- Check upstream Kubernetes for controller patterns

**When writing documentation**:
- Link to official docs rather than duplicating
- Reference API docs for CRD field descriptions
- Link to enhancements for feature context

**When debugging**:
- Check product docs for expected behavior
- Check Ignition/rpm-ostree docs for OS-level issues
- Check CRI-O docs for container runtime issues

## Related

- [OpenShift Ecosystem](./openshift-ecosystem.md) - Related repositories
- [Enhancement Index](./enhancement-index.md) - Feature enhancements
