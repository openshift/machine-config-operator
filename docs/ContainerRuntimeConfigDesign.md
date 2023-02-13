# Summary

Users need a way to update the container runtime configuration. Users can do this via MCO currently, but they need to know the correct options to use. We can hopefully help customers by documenting a CRD with the ContainerRuntime tuneables and have the MCO use these tuneables when rendering the crio.conf and storage.conf  files to Ignition/disk.

The CRI-O runtime is used by default and is the recommended solution.

# Motivation

The MCO contains the logic to write the crio.conf and storage.conf configuration files to Ignition. When Ignition starts on a machine it writes these three files to configure the container runtime. When these resources change within the MachineConfig, the new configs are rewritten and the MachineConfigDaemon is instructed to reboot the machine to pick up the new configs.

## Goals

The following goals should not require explicit knowledge of a customer to know the right command-line or configuration options to use within the ContainerRuntime.

1. Setting pids limit

2. Setting log level

3. Setting max log size

## Non-Goals

# Proposal

Extend the Machine Config Operator to include a ContainerRuntimeConfig CRD and ContainerRuntimeConfigController. By using a ContainerRuntimeConfig CRD there is an implicit allowlist of user controlled options for the container runtime. Upon deleting the ContainerRuntimeConfig instance the default config is restored.

## CRD

```
apiVersion: apiextensions.k8s.io/v1
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

## VALIDATION

It's important to note that, since the fields of the ContainerRuntimeConfig are directly read by the upstream kubernetes golang client, the validation of those values is handled directly by that golang client which is outside of the controller for ContainerRuntimeConfig. Please ensure the valid values are used for those fields as invalid values may render cluster nodes unusable.

### Troubleshooting Validation Issues

If the specified field values of the ContainerRuntimeConfig cannot be converted to their intended types, validation errors related may be visible on the ContainerRuntimeConfig CR. This is because such errors occur outside the coverage of the controller responsible for ContainerRuntimeConfig. In such case, users may need to look into the logs of the `machine-config-controller` container to get more information.

e.g.

```bash
$ oc logs -f -n openshift-machine-config-operator machine-config-controller-6fc64d9654-mdtv4
W0330 08:03:49.665463       1 reflector.go:436] github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/factory.go:101: watch of *v1.ContainerRuntimeConfig ended with: an error on the server ("unable to decode an event from the watch stream: unable to decode watch event: v1.ContainerRuntimeConfig.Spec: v1.ContainerRuntimeConfigSpec.MachineConfigPoolSelector: ContainerRuntimeConfig: v1.ContainerRuntimeConfiguration.OverlaySize: unmarshalerDecoder: quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$', error found in #10 byte of ...|\":\"9asadG\"},\"machine|..., bigger context ...|:{\"containerRuntimeConfig\":{\"overlaySize\":\"9asadG\"},\"machineConfigPoolSelector\":{\"matchLabels\":{\"cus|...") has prevented the request from succeeding
E0330 08:03:50.810155       1 reflector.go:138] github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/factory.go:101: Failed to watch *v1.ContainerRuntimeConfig: failed to list *v1.ContainerRuntimeConfig: v1.ContainerRuntimeConfigList.Items: []v1.ContainerRuntimeConfig: v1.ContainerRuntimeConfig.Spec: v1.ContainerRuntimeConfigSpec.MachineConfigPoolSelector: ContainerRuntimeConfig: v1.ContainerRuntimeConfiguration.OverlaySize: unmarshalerDecoder: quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$', error found in #10 byte of ...|":"9asadG"},"machine|..., bigger context ...|:{"containerRuntimeConfig":{"overlaySize":"9asadG"},"machineConfigPoolSelector":{"matchLabels":{"cus|...
```

## Example

This is what an example `ctrcfg` CR looks like. Note: you must make sure to add a label under `matchLabels` in the ContainerRuntimeConfig CR:

```
apiVersion: machineconfiguration.openshift.io/v1
kind: ContainerRuntimeConfig
metadata:
 name: set-pids-limit
spec:
 machineConfigPoolSelector:
   matchLabels:
     pools.operator.machineconfiguration.openshift.io/worker: ""
 containerRuntimeConfig:
   pidsLimit: 2048
```
Save your `ctrcfg` locally, for example as highpids.yaml

The label in the above example corresponds to the worker MachineConfigPool. By default the master/worker
MachineConfigPool has labels pools.operator.machineconfiguration.openshift.io/{worker|master}: "" in OCP 4.6 and later. If you have a custom pool, or have an earlier OCP version, you can instead create a label yourself as follows:

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

To roll out the pods limit changes to all the worker nodes (can switch this to master for the master nodes), add the label that you created, here: `custom-crio: high-pid-limit` under labels in the MachineConfigPool config:

```
oc edit machineconfigpool worker
```

Snippet of the MachineConfigPool config with the matching label added:

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

Now apply the `ctrcfg` that you created:


```
oc create -f highpids.yaml
```

Check that it was created:

```
$ oc get ctrcfg
NAME             AGE
set-pids-limit   50s
```

Check to ensure that a new 99-worker-containerruntime-managed is created and that a new rendered worker is created:
```
$ oc get machineconfigs
NAME                                  GENERATEDBYCONTROLLER                      IGNITIONVERSION   AGE
...
99-worker-containerrunetime-managed   fc45f8b73b2fc61e567f2111181d3e802f2565d7   3.1.0             7s
...
rendered-worker-45678XYZ              fc45f8b73b2fc61e567f2111181d3e802f2565d7   3.1.0             2s
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

The ContainerRuntimeConfigController would perform the following steps:

1. Validate the user defined ContainerRuntimeConfig

2. Render the current MachineConfigs (storage.files.contents[crio.conf, storage.conf]) into the ContainerRuntimeConfiguration structure

3. Load the ContainerRuntimeConfig from the passed in Spec.ContainerRuntimeConfig

4. Use mergo to merge the two structures

5. Serialize the ContainerRuntimeConfig to the respective toml files: storage.conf, and crio.conf

6. Create or Update the ignition /etc/containers/storage.conf and /etc/crio/crio.conf files within a 99-[role]-containerruntime-managed MachineConfig

After deletion of the ContainerRuntimeConfig instance the config will be reverted to the original storage and crio config.
