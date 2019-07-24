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
As of the time of this writing (OpenShift 4.2 development), you can set this field for "day 2" configuration
once a cluster has been created.  However, it will cause problems to set this in [install-time MachineConfig objects](https://github.com/openshift/installer/blob/master/docs/user/customization.md#install-time-customization-for-machine-configuration).  Further, changing this will incur an extra reboot when adding workers to a cluster.  For more information, see [this issue](https://github.com/openshift/machine-config-operator/issues/798).  It is currently planned to fix this for OpenShift 4.3.

### FIPS

This allows to enable/disable [FIPS mode](https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/7/html/security_guide/chap-federal_standards_and_regulations). If any of the configuration has FIPS enabled, it'll be set.  A similar restriction applies to this as for `KernelArguments` above.

### OSImageURL

You should not attempt to set this field; it is controlled by the operator and injected directly into the final `rendered-` config.
For more information, see [OSUpgrades.md].
