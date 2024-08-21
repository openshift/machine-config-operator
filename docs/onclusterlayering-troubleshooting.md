# On-Cluster Layering Troubleshooting

## Introduction

This is a guide to understanding why builds fail and what can be done to resume them as well as the identification of known issues with on-cluster layering.

## Troubleshooting Scenarios

### The `machine-os-builder` pod never starts

This could be a sign that the Machine Config Operator (MCO) is not in a good state. Ensure that none of the MachineConfigPools are degraded and that all of the secrets associated with the `MachineOSConfig` are present within the MCO namespace.

### Investigating and recovering from a build failure

In this scenario, a MachineOSBuild might report that an image build failed due to some reason. Whenever the build process starts, numerous ephemeral objects such as ConfigMaps and Secrets are created within the MCO namespace that are consumed by the build process. Whenever the build is successful, these objects are automatically deleted since they are no longer useful. However, in the event of a build failure, these objects are left in place to aid in the debugging process. We can leverage these objects to better understand what happened and why. These objects are created with the label `machineconfiguration.openshift.io/on-cluster-layering` which can be combined with a label selector for easy querying /cleanup.

#### Investigation

First, let's look at the build pod logs:

```console
$ oc logs pod/build-rendered-layered-677832916cb97f2524889bdf362d6a6f -n openshift-machine-config-operator -c image-build
...
time="2024-08-21T14:57:40Z" level=debug msg="setting uid"
time="2024-08-21T14:57:40Z" level=debug msg="Running &exec.Cmd{Path:\"/bin/sh\", Args:[]string{\"/bin/sh\", \"-c\", \"unknown-command\"}, Env:[]string{\"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\", \"HOSTNAME=efdc03708593\", \"HOME=/root\"}, Dir:\"/\", Stdin:(*os.File)(0xc000060038), Stdout:(*os.File)(0xc000060040), Stderr:(*os.File)(0xc000060048), ExtraFiles:[]*os.File(nil), SysProcAttr:(*syscall.SysProcAttr)(0xc000163810), Process:(*os.Process)(nil), ProcessState:(*os.ProcessState)(nil), ctx:context.Context(nil), Err:error(nil), Cancel:(func() error)(nil), WaitDelay:0, childIOFiles:[]io.Closer(nil), parentIOPipes:[]io.Closer(nil), goroutine:[]func() error(nil), goroutineErr:(<-chan error)(nil), ctxResult:(<-chan exec.ctxResult)(nil), createdByStack:[]uint8(nil), lookPathErr:error(nil)} (PATH = \"\")"
/bin/sh: line 1: unknown-command: command not found
```

Here, we can see that the Containerfile contains a reference to an unknown command. This could also be a syntax error or another issue with the build process.

To debug further, we can examine the full Containerfile that is consumed by the build process. To do this, we can retrieve it from the ConfigMap where it is stored:

```console
$ oc get configmap/dockerfile-rendered-layered-677832916cb97f2524889bdf362d6a6f -n openshift-machine-config-operator -o=jsonpath='{.data.Dockerfile}'
```

The above command returns the following:

```Dockerfile
# This Dockerfile is not intended to be directly built. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and templatized with
# certain options around base image pullspecs.
#
# Decode and extract the MachineConfig from the gzipped ConfigMap and move it
# into position. We do this in a separate stage so that we don't have the
# gzipped MachineConfig laying around.
FROM registry.ci.openshift.org/ocp/4.17-2024-08-21-021240@sha256:f686a353e75b2cf0981d69ee07233e71874ae0a1f37c10d883be070e63e5b60f AS extract
COPY ./machineconfig/machineconfig.json.gz /tmp/machineconfig.json.gz
RUN mkdir -p /etc/machine-config-daemon && \
        cat /tmp/machineconfig.json.gz | base64 -d | gunzip - > /etc/machine-config-daemon/currentconfig


# Pull our extensions image.
FROM registry.ci.openshift.org/ocp/4.17-2024-08-21-021240@sha256:4105156cf237ec1dcdcb38ea393099e5d83b3b91cd2a7c33f9abc8564b4cffd0 AS extensions



FROM registry.ci.openshift.org/ocp/4.17-2024-08-21-021240@sha256:f686a353e75b2cf0981d69ee07233e71874ae0a1f37c10d883be070e63e5b60f AS configs
# Copy the extracted MachineConfig into the expected place in the image.
COPY --from=extract /etc/machine-config-daemon/currentconfig /etc/machine-config-daemon/currentconfig
# Do the ignition live-apply, extracting the Ignition config from the MachineConfig.
# Not sure why Ignition explicitly requires the container env var to be set
# since it should be set by the container runtime / builder.
RUN container="oci" exec -a ignition-apply /usr/lib/dracut/modules.d/30ignition/ignition --ignore-unsupported <(cat /etc/machine-config-daemon/currentconfig | jq '.spec.config') && \
        ostree container commit

LABEL machineconfig=rendered-layered-677832916cb97f2524889bdf362d6a6f
LABEL machineconfigpool=layered
LABEL releaseversion=4.17.0-0.ci-2024-08-21-021240
LABEL baseOSContainerImage=registry.ci.openshift.org/ocp/4.17-2024-08-21-021240@sha256:f686a353e75b2cf0981d69ee07233e71874ae0a1f37c10d883be070e63e5b60f


FROM configs AS final
RUN unknown-command
```

Indeed, we can see that there is an unknown command used here. But what if the issue is with the rendered MachineConfig built into the image? Just like the Containerfile, the rendered MachineConfig used is temporarily stored in a ConfigMap as a gzipped and Base64-encoded blob. We can use `yq` and `gunzip` to view its contents:

```console
$ oc get configmap/mc-rendered-layered-677832916cb97f2524889bdf362d6a6f -n openshift-machine-config-operator -o yaml | yq '.data["machineconfig.json.gz"] | @base64d' | gunzip | yq --prettyPrint -o=yaml
```

```yaml
# Lines omitted for brevity
  kernelArguments:
    - systemd.unified_cgroup_hierarchy=1
    - cgroup_no_v1="all"
    - psi=0
  extensions: []
  fips: false
  kernelType: default
```

#### Recovery

Regardless of the cause of the build failure, recovering can be done with this procedure:

1. Remove all of the ephemeral build objects associated with the failed build, such as ConfigMaps and Secrets. These objects can be identified by the presence of the `machineconfiguration.openshift.io/on-cluster-layering` label, making cleanup easy:
    ```console
    # Delete the ConfigMaps
    $ oc get configmaps -n openshift-machine-config-operator -l 'machineconfiguration.openshift.io/on-cluster-layering=' -o name | xargs oc delete -n openshift-machine-config-operator

    # Delete the Secrets
    $ oc get secrets -n openshift-machine-config-operator -l 'machineconfiguration.openshift.io/on-cluster-layering=' -o name | xargs oc delete -n openshift-machine-config-operator

    # Delete the Build Pods
    $ oc get pods -n openshift-machine-config-operator -l 'machineconfiguration.openshift.io/on-cluster-layering=' -o name | xargs oc delete -n openshift-machine-config-operator
    ```
2. Delete the MachineOSConfig:
    ```console
    # Delete the MachineOSConfig
    $ oc delete machineosconfig/layered

3. Wait for the `machine-os-builder` pod to shut down.

4. Recreate the MachineOSConfig:
    ```console
    # Reapply the MachineOSConfig
    $ oc apply -f layered-machineosconfig.yaml
    ```

## Known Issues / Workarounds

This is not a complete or exhaustive list of known issues with on-cluster layering, but it can be a good place to start if you run into an issue.

### Cannot revert from layered OS to non-layered OS

We are still working on this and it has not yet landed.

### Rescheduled build pods indicate failure

If a build pod gets evicted due to a node cordon / drain or any other reason, the build will be marked as "failed". We are working on making this more resilient so that they will be automatically restarted after an eviction.

### Failed build recovery flow is not great

The user experience around this is not great right now, so it is a point for
improvement.

For this reason, when experimenting with a new custom Dockerfile, it is
recommended to create a new layered MachineConfigPool containing no nodes in
order to refine the build process. Once the build is successful, one can migrate
nodes into this new MachineConfigPool.

We are working on a more graceful recovery workflow.

### Takes a long time for the image to start building

If an existing MachineConfigPool is opted into on-cluster layering through the creation of a MachineOSConfig object, the build may take some time to start. This can be shortcutted by adding / removing a label from any node which will force the state loop to be executed.

### No proxy support

This is a known oversight that we have not yet implemented.

### Legacy-style secrets get out of sync

If you are using the internal image registry or an ImageStream and you are using the default `builder-dockercfg-` push / pull secret, this secret is periodically rotated every hour. Because we convert the secret from a legacy-style representation to a more modern representation, this secret can occasionally get out of sync. We are working to improve this situation by only reading the secret and doing the conversion right when it is used. However, it is still possible that the secret could get out-of-sync.

Therefore, it is highly recommended that you create a longer-lasting pull secret by using the following script (assumes you have KUBECONFIG set and the current context is the desired cluster):

```bash
#!/usr/bin/env bash

# Store this value in a local var for future use.
current_kubeconfig="$KUBECONFIG"

# Grab the external cluster URL from the provided KUBECONFIG.
cluster_url="$(yq '.clusters[0].cluster.server' "$KUBECONFIG")"

# Create a service account token with a 24-hour duration for the builder service
# account in the MCO namespace.
service_account_token="$(oc create token builder --duration=24h -n openshift-machine-config-operator)"

# Unset the KUBECONFIG env var so we don't mutate our kubeconfig.
unset KUBECONFIG

# Log into the Kube API server with the new service account token.
#
# NOTE: Depending on your setup, you may be prompted to answer yes or no about
# insecure connections.
oc login --token="$service_account_token" --server="$cluster_url"

# Log into the internal image registry and save the config into a local file.
oc registry login --to="$PWD/builder-dockerconfig.json"

# Using our original KUBECONFIG, create an image registry secret within the MCO
# namespace called "long-lived-builder-secret".
KUBECONFIG="$current_kubeconfig" oc create secret docker-registry long-lived-builder-secret --from-file=.dockerconfigjson="$PWD/builder-dockerconfig.json" -n openshift-machine-config-operator
```

## Known Limitations

- We do not provide a way to inject files into the build context. This is a
  deliberate design choice because the build system for on-cluster layering is not
  a general-purpose image builder nor a general-purpose CI system. A workaround is
  that if you need specific files within your build context, you can build and
  push a container off-cluster that has the required files in it. Your on-cluster
  build can then pull that container image and copy the necessary files out of it.

- Reverting from a layered-enabled MachineConfigPool is not currently
  supported in Tech Preview. If running in a cloud IPI environment such as AWS,
  GCP, Azure, et. al., one can delete the node and underlying machine, which
  will cause the cloud provider to replace the node. For example:

  ```bash
  #!/usr/bin/env bash

  node_name="$1"
  node_name="${node_name/node\//}"

  machine_id="$(oc get "node/$node_name" -o jsonpath='{.metadata.annotations.machine\.openshift\.io/machine}')"
  machine_id="${machine_id/openshift-machine-api\//}"

  oc delete --wait=false "machine/$machine_id" -n openshift-machine-api
  oc delete --wait=false "node/$node_name"
  ```

- We do not currently ingest OS base images from the osImageURL field.

- Multi-arch is not currently supported, but will be in the future.

- Upgrades are currently untested, but will be supported in the future.

- MachineConfig changes which require the extensions container (i.e.,
  enabling realtime kernels, et. al.) are not currently supported,
  but will be in the future.

- HyperShift is currently untested / unsupported, but will be in the
  future.