# Summary

Users need a way to update Kubelet configuration and Kubelet tuneables. Users can do this via MCO currently, but they need to know the correct options to use. We can hopefully help customers by documenting a CRD with the Kubelet tuneables and have the MCO use these tuneables when rendering the kubelet.conf and kubelet.system files to Ignition/disk.

Runtime configuration is configured via Kubelet as well. The CRI-O runtime is used by default and is the recommended solution. Some customers may have Docker specific tools or monitoring products they have paid for.

# Motivation

The MCO contains the logic to write the kubelet.conf configuration file and kubelet.system systemd unit file to Ignition. When Ignition starts on a machine it writes these two files to configure the kubelet. When these resources change within the MachineConfig, the new configs are rewritten and the MachineConfigDaemon is instructed to reboot the machine to pick up the new configs.

## Goals

The following goals should not require explicit knowledge of a customer to know the right command-line or configuration options to use within the Kubelet.

1. changing out of resource handling ([https://docs.openshift.com/container-platform/3.11/admin_guide/out_of_resource_handling.html#out-of-resource-create-config](https://docs.openshift.com/container-platform/3.11/admin_guide/out_of_resource_handling.html#out-of-resource-create-config))

2. setting a feature gate ([https://docs.openshift.com/container-platform/3.11/install_config/configuring_ephemeral.html#ephemeral-storage-enabling-ephemeral-storage](https://docs.openshift.com/container-platform/3.11/install_config/configuring_ephemeral.html#ephemeral-storage-enabling-ephemeral-storage))

3. setting cpu manager to static ([https://docs.openshift.com/container-platform/3.11/scaling_performance/using_cpu_manager.html](https://docs.openshift.com/container-platform/3.11/scaling_performance/using_cpu_manager.html))

4. setting max pods per node ([https://docs.openshift.com/container-platform/3.11/admin_guide/manage_nodes.html#admin-guide-max-pods-per-node](https://docs.openshift.com/container-platform/3.11/admin_guide/manage_nodes.html#admin-guide-max-pods-per-node))

5. configuring garbage collection ([https://docs.openshift.com/container-platform/3.11/admin_guide/garbage_collection.html](https://docs.openshift.com/container-platform/3.11/admin_guide/garbage_collection.html))

6. configuring node resources ([https://docs.openshift.com/container-platform/3.11/admin_guide/manage_nodes.html#configuring-node-resources](https://docs.openshift.com/container-platform/3.11/admin_guide/manage_nodes.html#configuring-node-resources))

## Non-Goals

# Proposal

Extend the Machine Config Operator to include a KubetletConfig CRD and KubeletConfigController. By using a KubeletConfig CRD there is an implicit allowlist of allowed user controlled options. Upon deleting the KubeletConfig instance the default config is restored.

## CRD

```
apiVersion: apiextensions.k8s.io/v1beta1
kind: CustomResourceDefinition
metadata:
  name: kubeletconfigs.machineconfiguration.openshift.io
spec:
  group: machineconfiguration.openshift.io
  versions:
    - name: v1
      served: true
      storage: true
  scope: Cluster
  names:
    plural: kubeletconfigs
    singular: kubeletconfig
    kind: KubeletConfig
```

## SPEC

```
MachineConfigPoolSelector *metav1.LabelSelector
Runtime:
  Name  string
  Endpoint string
KubeletConfig
  [KubeletConfigurationSpec](https://github.com/kubernetes/kubernetes/blob/release-1.11/pkg/kubelet/apis/kubeletconfig/v1beta1/types.go#L45)
```

## Example
This is what an example `kubelet config` CR looks like. Note: you must make sure to add a label under `matchLabels` in the KubeletConfig CR:

```
apiVersion: machineconfiguration.openshift.io/v1
kind: KubeletConfig
metadata:
  name: set-max-pods
spec:
  logLevel: 5
  machineConfigPoolSelector:
    matchLabels:
      pools.operator.machineconfiguration.openshift.io/worker: ""
  kubeletConfig:
    maxPods: 100
```

The logLevel attribute is optional and will default to level 2. Increasing the logLevel
uses more IO, CPU, and resources on all the machines within the pool.

Save your `kubeletconfig` locally, for example as maxpods.yaml

The label in the above example corresponds to the worker MachineConfigPool. By default the master/worker
MachineConfigPool has labels pools.operator.machineconfiguration.openshift.io/{worker|master}: "" in OCP 4.6 and later. If you have a custom pool, or have an earlier OCP version, you can instead create a label youself as follows:

```
apiVersion: machineconfiguration.openshift.io/v1
kind: KubeletConfig
metadata:
  name: set-max-pods
spec:
  logLevel: 5
  machineConfigPoolSelector:
    matchLabels:
      custom-kubelet: small-pods
  kubeletConfig:
    maxPods: 100
```

To roll out the pods limit changes to all the worker nodes (can switch this to master for the master nodes), add the label that you created, here: `custom-kubelet: small-pods` under labels in the machineConfigPool config: 

```
oc edit machineconfigpool worker
```

Snippet of the machineConfigPool config with the matching label added:

```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfigPool
metadata:
  creationTimestamp: 2019-04-10T16:39:39Z
  generation: 1
  labels:
    custom-kubelet: small-pods
  name: worker
  ...
```
Now apply the `kubeletconfig` that you created:

```
$ oc apply -f maxpods.yaml
kubeletconfig.machineconfiguration.openshift.io/set-max-pods created
```

Double check that it was created:

```
$ oc get kubeletconfig
NAME           AGE
set-max-pods   6s
```

Check to ensure that a new 99-worker-XXX-kubelet is created and that a new rendered worker is created:

```
$ oc get machineconfigs
NAME                                 GENERATEDBYCONTROLLER                      IGNITIONVERSION   AGE
...
99-worker-kubelet-managed            fc45f8b73b2fc61e567f2111181d3e802f2565d7   3.1.0             7s
...
rendered-worker-45678XYZ             fc45f8b73b2fc61e567f2111181d3e802f2565d7   3.1.0             2s
...
```

The changes should now be rolled out to each node in the worker pool via that new rendered-worker machine config. You can verify by checking 
that the latest rendered-worker machine-config has been rolled out to the pools successfully:

```
$ oc get mcp
NAME     CONFIG                     UPDATED   UPDATING   DEGRADED   MACHINECOUNT   READYMACHINECOUNT   UPDATEDMACHINECOUNT   DEGRADEDMACHINECOUNT   AGE
...
worker   rendered-worker-45678XYZ   True      False      False      3              3                   3                     0                      5m
...
```

## Implementation Details

The KubeletConfigController would perform the following steps:

1. Validate the user defined KubeletConfig

2. Render the current MachineConfig (storage.files.contents[kubelet.conf]) into the KubeletConfiguration structure

3. Load the KubeletConfig from the passed in Spec.KubeletConfig

4. Use mergo to merge the two structures

5. Serialize the KubeletConfig to json

6. Create or Update an ignition /etc/kubernetes/kubelet.conf file within a 99-[role]-kubelet-managed MachineConfig

After deletion of the KubeletConfig instance the config will be reverted to the original kubelet config.

## Runtime Selection

### Requirements

First time installs will **always** install with CRI-O. After the initial install, the user will be able to switch from CRI-O to Docker, or from Docker to CRI-O if commanded to.

Setting the options with the KubeletConfig CRD will cause the node to drain and reboot. Upon reboot, the machine will be reconfigured to use docker as the runtime via Machine Config Daemon and a rollout to each node.

**_Note: Docker is only available in Bring-Your-Own-RHEL (User Provisioned Infrastructure) configurations._**

TODO: Network Daemon?

TODO: crictl configuration?

### Runtime Partition Requirements

There are specific partitioning schemes that are supported.

1. Single Root Partition (/): Single partitioning scheme where everything is on a root partition

2. Control Partition (/var): There is a root (/) partition for the filesystem, in addition to a separate /var disk or partition for runtime specific data. This scheme is useful in cases where the customer wants a small root partition or run the runtime via another disk (ie: SSD)

3. Control and Log Partition (/var and /var/log): There is a root (/) partition for the filesystem, in addition to a separate /var and /log disk or partition. This scheme is useful in cases where the customer wants to protect from log files filling up the primary image or runtime disk.
