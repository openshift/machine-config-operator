# MachineConfig

## Goal

1. MachineConfig objects should be complete source of machine configuration at install / first-boot for a machine.

2. MachineConfig objects should be the source of machine configuration during upgrades.

3. The MachineConfig served to the machines should be static and there should be no links to remote locations for dynamic configuration. This includes both remote sources for Ignition and remote files.

4. Define a way to merge multiple MachineConfigs.

## Non Goals

1. Updates from the remote sources for Ignition config will not be actively applied.

## Overview

The machines using `RHCOS` will be configured using an Ignition config served to the machines at first boot. An in-cluster Ignition endpoint will serve these Ignition configs to new machines based on the MachineConfig object defined.
Also during upgrades the MachineConfigDaemon running on these machines will use the defined MachineConfig object to upgrade the machine's configurations.

Users will not be allowed to change the MachineConfig object defined by openshift. Although, users will create new MachineConfig objects for their customization. Therefore the MachineConfig object used by the in-cluster Ignition server and daemon running on the machines has to be a merged version.

The MachineConfig object used by in-cluster Ignition server and daemon running on the machine should be a static definition of all the resources. Users might create MachineConfig objects that have links to remote Ignition configs, but at merge time, a snapshot of the remote Ignition config should be used to create the merged MachineConfig. The same is valid for remote files, at merge time the files are fetched and the contents are verified and replaced in-place the final merged MachineConfig.

## Detailed Design

### Final Rendered MachineConfig object

MachineConfig objects can be created by both the OpenShift platform and users to define machine configurations.  There is a final "rendered" MachineConfig object (prefixed with `rendered-`) that is the union of its inputs.

1. The rendered MachineConfig object contains merged spec of all the different MachineConfig objects that are valid for the machine.

2. To ensure the configuration does not change unexpectedly between usage, all remote content referenced in the ignition config is retrieved and embedded into the merged MachineConfig at the time of generation.

### No remote sources in rendered MachineConfig

To ensure all the machines see the same configurations, remote sources need to be resolved to a snapshot at generation time.

### MachineConfig definition

```go
type MachineConfig struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec MachineConfigSpec `json:spec`
}

type MachineConfigSpec struct {
    // Config is a Ignition Config object.
    Config ign.Config `json:"config"`
    KernelArguments []string `json:"kernelArguments"`
    Fips bool `json:"fips"`
    KernelType string `json:"kernelType"`
}
```

The actual custom resource manifest then could look like this:

```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: worker-2864988432
spec:
  config:
    ignition:
      version: 2.2.0
    storage:
      files:
      - contents:
          source: data:,%20
        filesystem: root
        mode: 384
        path: /root/myfile
```

Notice how it's the usual Ignition config object *inplace*, not as a JSON
string -- also note the casing follows the `json:` markers in the definition above, of course this follows for the
Ignition config keys as well.

### How to create generated MachineConfig

1. For each MachineConfig object,

    a. Use the Ignition's `internal` package to render a valid Ignition config. Ignition render fetches remote sources and then appends/replaces inline config. [More Info](https://github.com/coreos/Ignition/blob/99b8d5052db6b30fe4812d2efbc8713a3abbef70/internal/exec/engine.go#L204-L252)

    b. Ignition does not fetch remote sources for files in render. Run [fetcher](https://github.com/coreos/Ignition/blob/99b8d5052db6b30fe4812d2efbc8713a3abbef70/internal/exec/util/file.go#L183) separately to load the remote files.

2. Use Ignition's [append](https://github.com/coreos/ignition/blob/99b8d5052db6b30fe4812d2efbc8713a3abbef70/config/v2_2/append.go#L23-L34) to merge all the Ignition configs generated above.

    * Use the openshift defined Ignition config as base and append all the other Ignition configs in a pre-defined order.

### KernelArguments

This extends the host's kernel arguments.  Use this for e.g. [nosmt](https://access.redhat.com/solutions/rhel-smt).
As of OpenShift 4.3, you can set this field for either "day 1" (install time) or "day 2" configuration
once a cluster has been created. See [installer docs](https://github.com/openshift/installer/blob/master/docs/user/customization.md#nodes-with-custom-kernel-arguments) on how to set them as install-time MachineConfig objects. For "day 2" operation you can also add them as a similar machineconfig, incurring a reboot.

Example MachineConfig to change kernel log level:
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: "master"
  name: 99-master-kargs-loglevel
spec:
  kernelArguments:
    - 'loglevel=7'
```

Note that for 4.2 clusters this is only supported as a "day 2" operation.

#### Known Issue Affecting 4.2 Clusters
On a 4.2 based OCP cluster if we already have kernel arguments applied using MachineConfig and then we try to create a new node using openshift-machine-api, existing kargs won't get applied. This behaviour is because 4.2 doesn't know how to process kernel arguments during firstboot on a newly spun node. See [bug#1766346](https://bugzilla.redhat.com/show_bug.cgi?id=1766346) for more information.

The best way to fix this issue is by getting the bootimage updated to 4.3 or later version which can be tracked at [enhancement proposal](https://github.com/openshift/enhancements/pull/201/) .

Until we have the enhancement implemented it can be done manually by updating the bootimage in machineset.

**Example:**

Suppose we have a 4.2 cluster created on AWS and have applied karg `foo` to the cluster with the following MachineConfig:
```
$ cat worker-karg.yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: worker-kargs
spec:
  kernelArguments:
    - foo
```
The first step is to upgrade the cluster to OCP 4.3 or later version. Once the cluster has been successfully upgraded, list the available machinesets:
```
$ oc get machineset -n openshift-machine-api
NAME                                          DESIRED   CURRENT   READY   AVAILABLE   AGE
ci-ln-bx6fgqt-d5d6b-89fn7-worker-us-east-1b   2         2         2       2           79m
ci-ln-bx6fgqt-d5d6b-89fn7-worker-us-east-1c   1         1         1       1           79m
```

In our example, let's edit the bootimage in machineset ci-ln-bx6fgqt-d5d6b-89fn7-worker-us-east-1c with corresponding 4.3 bootimage. For 4.4, a list of bootimages for different platforms and regions are available [here](https://github.com/openshift/installer/blob/release-4.4/data/data/rhcos.json), checkout the respective OCP release branch in the repo for the corresponding release. Since the cluster was created in AWS in the us-east-1 region, we will update the ami id to [ami-0543fbfb4749f3c3b](https://github.com/openshift/installer/blob/release-4.4/data/data/rhcos.json#L43) with the following command:

```
$ oc edit machineset ci-ln-bx6fgqt-d5d6b-89fn7-worker-us-east-1c -n openshift-machine-api
 ```
Similarly, we can update the bootimage for the rest of the machinesets too.
Once the bootimage has been updated, create a node by scaling up machineset replicas.
```
$ oc scale --replicas=2 machineset ci-ln-9jk9j3b-d5d6b-kw7lr-worker-us-east-1c -n openshift-machine-api
```

#### nosmt
When a machine boots with `nosmt` Kernel Argument, it disables multi-threading on that host and the system will only utilize physical CPU cores. While applying `nosmt` on any node in the cluster, ensure that enough CPU resources are available to schedule all pods, otherwise it can lead to a degraded cluster. For example: a basic 3 master and 3 worker node cluster having 2 physical CPU cores on each node should be fine.

### KernelType

This feature is available with OCP 4.4 and onward releases as both `day 1` and `day 2` operation. It allows to choose between traditional and Real Time (RT) kernel on an RHCOS node. Supported values are
`""` or `default` for traditional kernel and `realtime` for RT kernel.

To set kernelType field during cluster install, see the [installer guide](https://github.com/openshift/installer/blob/master/docs/user/customization.md#Switching-RHCOS-host-kernel-using-KernelType).

For day 2, create a MachineConfig and apply to the cluster using `oc create -f`

Example MachineConfig to switch to RT kernel on worker nodes:
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: "worker"
  name: worker-kerneltype
spec:
  kernelType: realtime
```

**Note:** The RT kernel lowers throughput (performance) in return for improved worst-case latency bounds. This feature is intended only for use cases that require consistent low latency. For more information, see the [Linux Foundation wiki](https://wiki.linuxfoundation.org/realtime/start) and the [RHEL RT portal](https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux_for_real_time/8/).

### FIPS

This allows to enable/disable [FIPS mode](https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/7/html/security_guide/chap-federal_standards_and_regulations). If any of the configuration has FIPS enabled, it'll be set.  A similar restriction applies to this as for `KernelArguments` above.

### OSImageURL

You should not attempt to set this field; it is controlled by the operator and injected directly into the final `rendered-` config.
For more information, see [OSUpgrades.md](OSUpgrades.md).
