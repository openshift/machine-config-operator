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

### Generated MachineConfig object

MachineConfig objects can be created by both openshift and users to define machine configurations. But the MachineConfig object used by the MachineConfig Server and daemon running on machines is a single generated MachineConfig object.

1. The generated MachineConfig object contains merged spec of all the different MachineConfig objects that are valid for the machine.

2. To ensure the configuration does not change unexpectedly between usage, all remote content referenced in the ignition config is retrieved and embedded into the merged MachineConfig at the time of generation.

### No remote sources in generated MachineConfig

To ensure all the machines see the same configurations, remote sources need to be resolved to a snapshot at generation time.

### MachineConfig definition

```go
type MachineConfig struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec MachineConfigSpec `json:spec`
}

type MachineConfigSpec struct {
    // OSImageURL specifies the remote location that will be used to
    // fetch the OS. This must be in the canonical $name@$digest format.
    OSImageURL string `json:"osImageURL"`
    // Config is a Ignition Config object.
    Config igntypes.Config `json:"config"`
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
  osImageURL: quay.io/openshift/rhcos@sha256:...
```

(Notice how it's the usual Ignition config object *inplace*, not as a JSON
string -- also note the `config` and `osImageURL` casing follow the
`json:` markers in the definition above, of course this follows for the
Ignition config keys as well).

### How to create generated MachineConfig

1. For each MachineConfig object,

    a. Use the Ignition's `internal` package to render a valid Ignition config. Ignition render fetches remote sources and then appends/replaces inline config. [More Info](https://github.com/coreos/Ignition/blob/99b8d5052db6b30fe4812d2efbc8713a3abbef70/internal/exec/engine.go#L204-L252)

    b. Ignition does not fetch remote sources for files in render. Run [fetcher](https://github.com/coreos/Ignition/blob/99b8d5052db6b30fe4812d2efbc8713a3abbef70/internal/exec/util/file.go#L183) separately to load the remote files.

2. Use Ignition's [append](https://github.com/coreos/ignition/blob/99b8d5052db6b30fe4812d2efbc8713a3abbef70/config/v2_2/append.go#L23-L34) to merge all the Ignition configs generated above.

    * Use the openshift defined Ignition config as base and append all the other Ignition configs in a pre-defined order.

### OSImageURL

The operating system used to first boot a machine is platform dependent. For example, on AWS AMIs are used to bring up EC2Instances. But for day-2 updates of the cluster, the MachineConfigDaemon uses the `OSImageURL` to fetch new operating system during updates. An example for OSImageURL is `quay.io/openshift/$CONTAINER@sha256:$DIGEST`. The digest is required to ensure there are no race conditions.

When combining multiple MachineConfig objects, OSImageURL field is ignored from all the MachineConfig objects except the one defined by Openshift.
