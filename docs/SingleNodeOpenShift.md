# Single Node OpenShift

Single Node OpenShift (SNO) clusters have a single control-plane node and are not considered HA, whereas OpenShift clusters with multiple control-plane nodes are considered HA as control-plane functions can be rescheduled on another node.

## Differences

There are several key differences in how the Machine Config Daemon handles non-HA/single node clusters:

### Topology

MCO reads the `controlPlaneTopology` value set in the [infrastructure cluster object](https://github.com/openshift/api/blob/master/config/v1/0000_10_config-operator_01_infrastructure.crd.yaml#L182) and copies that value into the `ControllerConfig` object. Later, the Node controller reads the `controlPlaneTopology` value from `ControllerConfig` and adds an annotation (`machineconfiguration.openshift.io/controlPlaneTopology`) to all Node objects to describe the `controlPlaneTopology` of the cluster.

A non-HA/single node cluster is annotated with `SingleReplica`. In the absence of this annotation, the Machine Config Daemon defaults to `HighlyAvailable`. This primarily affects the node drain behavior as discussed below.

### Node Drain Behavior

In a non-HA/single node cluster, a node drain will **not** occur. This differs from HA clusters, where a [node drain](./MachineConfigDaemon.md#node-drain) will occur whenever required. 

### Authentication and API Availability

In a non-HA cluster, all authentication and API services will be unavailable until the node finishes rebooting. If those services do not come back up due to a failed configuration change or update, debugging may require SSH or console access to the node.

With the exception of [rebootless updates](./MachineConfigDaemon.md#rebootless-updates), any MachineConfig changes will be disruptive due to the need to reboot the node.

## Supported Functionalities

All functionalities provided by the [MachineConfig API](./MachineConfiguration.md) and [MachineConfigDaemon](./MachineConfigDaemon) are currently supported.

## Upgrades

With 4.9, Single Node OpenShift upgrades-in-place is supported with the exception of [post reboot cluster safety](#Post-reboot-cluster-safety) issue.

## Post reboot cluster safety
Any configuration applied using MCO on node including upgrades requires node reboot with the exception of [rebootless updates](./MachineConfigDaemon.md#rebootless-updates). MCO doesn't provide guarantee for post-reboot cluster safety. This implies that MCO will try its best to keep feature parity and behavior on SNO similar to regular OCP cluster. But due to the nature of SNO (single control plane node), when control plane node reboots during update/upgrade, node may not come up or join cluster due to multiple reasons that is not related to MCO, such as:

- Bug in one of the package that was updated
- Faulty config applied on node by user
- Manual ssh into node by user and performing config change
- Bug in another OCP component causing cluster not being fully available

In these scenarios it is harder to find the exact cause of a problematic node post-reboot, and as a consequence it is harder to debug and recover from these cases. For the MCO to help debug, we would need easily reproducible steps, or system logging that points to MCO being the cause of the issue.
