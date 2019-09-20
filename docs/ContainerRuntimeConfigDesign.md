# Summary 

Users need a way to update the container runtime configuration. Users can do this via MCO currently, but they need to know the correct options to use. We can hopefully help customers by documenting a CRD with the ContainerRuntime tuneables and have the MCO use these tuneables when rendering the crio.conf and storage.conf  files to Ignition/disk.

The CRI-O runtime is used by default and is the recommended solution.

# Motivation

The MCO contains the logic to write the crio.conf and storage.conf configuration files to Ignition. When Ignition starts on a machine it writes these three files to configure the container runtime. When these resources change within the MachineConfig, the new configs are rewritten and the MachineConfigDaemon is instructed to reboot the machine to pick up the new configs.

## Goals

The following goals should not require explicit knowledge of a customer to know the right command-line or configuration options to use within the ContainerRuntime.

1. setting pids limit

2. setting log level

3. Setting max log size

## Non-Goals

# Proposal

Extend the Machine Config Operator to include a ContainerRuntimeConfig CRD and ContainerRuntimeConfigController. By using a ContainerRuntimeConfig CRD there is an implicit whitelist of user controlled options for the container runtime. Upon deleting the ContainerRuntimeConfig instance the default config is restored.

## CRD

```
apiVersion: apiextensions.k8s.io/v1beta1
kind: CustomResourceDefinition
metadata:
 name: containerruntimeconfigs.machineconfiguration.openshift.io
spec:
 group: machineconfiguration.openshift.io
 versions:
   - name: v1
     served: true
     storage: true
 scope: Cluster
 names:
   plural: containerruntimeconfigs
   singular: containerruntimeconfig
   kind: ContainerRuntimeConfig
   short: ctrcfg
```

## SPEC

```
MachineConfigPoolSelector *metav1.LabelSelector
Runtime:
 Name  string
 Endpoint string
ContainerRuntimeConfig
 [ContainerRuntimeConfigurationSpec](Will have to add this in this repo)
```

## Example

This is what an example `ctrcfg` CR would look like:

```
apiVersion: machineconfiguration.openshift.io/v1
kind: ContainerRuntimeConfig
metadata:
 name: set-pids-limit
spec:
 machineConfigPoolSelector:
   matchLabels:
     custom-crio: high-pid-limit
 containerRuntimeConfig:
   pidsLimit: 2048
```

Make sure to add a label under `matchLabels` in the ContainerRuntimeConfig CR and use that label in the MachineConfigPool config that you want the changes rolled out to. From the example above, that label would be `custom-crio: high-pid-limit`.

To roll out the pids limit changes to all the worker nodes (can switch this to master for the master nodes), add `custom-crio: high-pid-limit` under labels in the machineConfigPool config.  

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
    custom-crio: high-pid-limit
  name: worker
  ...
```

## Implementation Details

The ContainerRuntimeConfigController would perform the following steps:

1. Validate the user defined ContainerRuntimeConfig

2. Render the current MachineConfigs (storage.files.contents[crio.conf, storage.conf]) into the ContainerRuntimeConfiguration structure

3. Load the ContainerRuntimeConfig from the passed in Spec.ContainerRuntimeConfig

4. User mergo to merge the two structures

5. Serialize the ContainerRuntimeConfig to the respective toml files: storage.conf, and crio.conf

5. Create or Update the ignition /etc/containers/storage.conf and /etc/crio/crio.conf files within a 99-[role]-containerruntime-managed MachineConfig

After deletion of the ContainerRuntimeConfig instance the config will be reverted to the original storage and crio config.
