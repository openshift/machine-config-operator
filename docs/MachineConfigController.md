# MachineConfigController

## Goals

1. Coordinate upgrade of machines to desired configurations defined by a `MachineConfig` object.

2. Provide options to control upgrade for `sets of machines` individually.

## Non Goals

1. MachineConfigController is not responsible for updating the machines directly.

2. MachineConfigController is not responsible for serving Ignition configs to new machines trying to join the cluster.

## Sub Controllers

1. `TemplateController` is responsible for generating the MachineConfigs for pre-defined roles of machines from internal templates based on cluster configuration.

2. `UpdateController` is responsible for upgrading machines to desired MachineConfig by coordinating with a daemon running on each machine.

3. `RenderController` is responsible for discovering MachineConfigs for a `Pool of Machines` and generating the static MachineConfig.

## MachinePool

```go
type MachinePool struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec MachinePoolSpec `json:"spec"`
    Status MachinePoolStatus `json:"status"`
}

type MachinePoolSpec struct {
    // Label selector for MachineConfigs.
    // Refer https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/ on how label and selectors work.
    MachineConfigSelector *metav1.LabelSelector `json:"machineConfigSelector,omitempty"`

    // Label selector for Machines.
    MachineSelector *metav1.LabelSelector `json:"machineSelector,omitempty"`

    // If true, changes to this machine pool should be stopped.
    // This includes generating new desiredMachineConfig and update of machines.
    Paused bool `json:"paused"`

    // MaxUnavailable specifies the percentage or constant number of machines that can be updating at any given time.
    // default is 1.
    MaxUnavailable *intstr.IntOrString `json:"maxUnavailable"`
}

type MachinePoolStatus struct {
    // The generation observed by the controller.
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // The current MachineConfig object for the machine pool.
    CurrentMachineConfig string `json:"currentMachineConfig"`

    // Total number of machines in the machine pool.
    MachineCount int32 `json:"machineCount"`

    // Total number of machines targeted by the pool that have the CurrentMachineConfig as their config.
    UpdatedMachineCount int32 `json:"updatingMachines"`

    // Total number of ready machines targeted by the pool.
    ReadyMachineCount int32 `json:"readyMachines"`

    // Total number of unavailable (non-ready) machines targeted by the pool.
    // A node is marked unavailable if it is in updating state or NodeReady condition is false.
    UnavailableMachineCount int32 `json:"unavailableMachines"`

    // Represents the latest available observations of current state.
    Conditions []MachinePoolConditions `json:"conditions"`
}
```

## MachineSets vs MachinePool

- MachineSets describe nodes with respect to cloud / machine provider. MachinePool allows MachineConfigController components to define and provide status of machines in context of upgrades.

- MachinePool also allows users to configure how upgrades are rolled out to the machines in a pool.

- MachineSelector can be replaced with reference to MachineSet.

## TemplateController

The TemplateController uses `internal templates` and a `configuration object` to generate OpenShift owned MachineConfig objects for pre-defined `roles` like master, worker etc.

- The TemplateController constantly reconciles the MachineConfig objects in the cluster to match its internal state. The TemplateController will overwrite any user changes of its owned objects.

- TemplateController watches changes to the `MachineConfigControllerConfig` to generate OpenShift owned MachineConfig objects.

- TemplateController adds `OwnerReference` or similar annotations on its objects to declare ownership.

## RenderController

The RenderController generates the desired MachineConfig object based on the MachineConfigSelector defined in MachinePool.

- RenderController watches for changes on MachinePool object to find all the MachineConfig objects using `MachineConfigSelector` and updating the `CurrentMachineConfig` with the generated MachineConfig.

- RenderController watches for changes on all the MachineConfig objects and syncs all the MachinePool objects with new `CurrentMachineConfig`.

### Finding MachineConfigs

Use kubernetes Deployment behavior for LabelSelector to find Pods.

### Generating desired MachineConfig

Use the merging behavior defined in MachineConfig design document [here](./MachineConfiguration.md#how-to-create-generated-machineconfig) to create a single MachineConfig from all the MachineConfig object that were selected above.

#### Ordering the MachineConfigs

The render controller sorts all the other MachineConfigs based on the lexicographically increasing order of their `Name`. It uses the first MachineConfig in the list as the base and appends the rest to the base MachineConfig.

## UpdateController

The UpdateController coordinates upgrade for machines in a machine pool. UpdateController uses annotations on node objects to coordinate with the `MachineConfigDaemon` running on each machine to upgrade each machine to the desired Machine Configuration.

UpdateController watches for changes on MachinePool and runs update if,

1. If the `.Status.CurrentMachineConfig` has been updated.

2. If new nodes can be updated to the current configuration as new Machines are available with old configuration if permitted by `NodeLimit` or the `NodeLimit` has increased allowing more node to be updated.

**Historically** the following annotations were used to coordinate between UpdateController and the MachineConfigDaemon,

* node-configuration.v1.coreos.com/currentConfig
* node-configuration.v1.coreos.com/targetConfig
* node-configuration.v1.coreos.com/desiredConfig

With these three fields it becomes possible to determine the update progress of the machine:

* desiredConfig == currentConfig: The machine is up-to-date.
* desiredConfig != currentConfig && desiredConfig == targetConfig: The machine is not up-to-date, but is in the process of updating.
* desiredConfig != currentConfig && desiredConfig != targetConfig: The machine is not up-to-date and is not in the process of updating.
* Node is marked updated by UpdateController unless `NodeReady` is reported by kubelet.

## UpdateController interface with MachineConfigDaemon

Following annotations on node object will be used by UpdateController to coordinate node update with MachineConfigDaemon.

* machine-config-daemon.v1.openshift.com/currentConfig : defines the current MachineConfig applied by MachineConfigDaemon.
* machine-config-daemon.v1.openshift.com/desiredConfig : defines the desired MachineConfig that need to be applied by MachineConfigDaemon
* machine-config-daemon.v1.openshift.com/state : defines the state of the MachineConfigDaemon, It can be done, working and degraded.

With these three fields it becomes possible to determine the update progress of the machine:

a. desiredConfig == currentConfig : The machine is up-to-date.
b. desiredConfig != currentConfig && state == working : The machine is not up-to-date, but is in the process of updating.
c. desiredConfig != currentConfig && state == degraded : The machine is not up-to-date and MachineConfigDaemon cannot apply the desired configuration.

Node is marked updated by UpdateController only when `NodeReady` is reported by kubelet when case (a) is true.
