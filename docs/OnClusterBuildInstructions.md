# On-Cluster OS Image Builds

## Prerequisites

- OpenShift Cluster w/ 4.14.

- Admin privileges on the aforementioned cluster

- An image registry that one has push and pull permissions to, which will be
  used for storing the final OS image. Pull permissions for this image registry
  must be added to the global pull secret. See [these
  instructions](https://docs.openshift.com/container-platform/4.13/openshift_images/managing_images/using-image-pull-secrets.html#images-update-global-pull-secret_using-image-pull-secrets)
  for how to do this. One can also use an OpenShift ImageStream within the MCO namespace for this purpose.

## Initial Setup

1. Obtain the following information:

    1. _(Optional)_ Create an OpenShift ImageStream in the MCO namespace:

       ```console
       $ oc create imagestream os-image -n openshift-machine-config-operator
       ```

    2. The name of a pull secret for the base OS image and the
       extensions container. This must be created as a Secret within
       the MCO namespace. One can clone the global pull secret, if
       desired:

       ```console
       $ oc create secret docker-registry global-pull-secret-copy -n openshift-machine-config-operator --from-file=.dockerconfigjson=<(oc get secret/pull-secret -n openshift-config -o json | jq -r '.data[".dockerconfigjson"] | @base64d')
       ```

    3. The final image pullspec. For example:
       `quay.io/myorg/myrepo:latest`. Any provided tags will be
       ignored. Instead, images will be tagged with the name of the
       rendered MachineConfig that was used to produce the image. For
       example:
       `quay.io/myorg/myrepo:rendered-worker-dd37180cefa6a1e88834966330c0c028`. If using an OpenShift Imagestream for this purpose, the image pullspec may be obtained thusly:

       ```console
       $ oc get imagestream/os-image -n openshift-machine-config-operator -o=jsonpath='{.status.dockerImageRepository}'
       image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image
       ```

    4. The name of a push secret that will enable the final OS image to
       be pushed to the above pullspec. This must be created as a
       Secret within the MCO namespace. If using an OpenShift ImageStream, one can use the builder secret for the MCO namespace. Its name may be obtained thusly:

       ```console
       $ oc get secrets -n openshift-machine-config-operator -o name | grep "builder-dockercfg"
       secret/builder-dockercfg-g8mgj
       ```

2. Create the `on-cluster-build-config` ConfigMap in the MCO namespace
   with the information obtained above:

   ```yaml
   ---
   apiVersion: v1
   kind: ConfigMap
   metadata:
     name: on-cluster-build-config
     namespace: openshift-machine-config-operator
   data:
     baseImagePullSecretName: global-pull-secret-copy
     finalImagePushSecretName: final-image-push-secret
     finalImagePullspec: "quay.io/myorg/myrepo:latest"
   ```

3. Create a new MachineConfigPool for layering use:  

   ```yaml
   ---
   apiVersion: machineconfiguration.openshift.io/v1
   kind: MachineConfigPool
   metadata:
     name: layering
   spec:
     machineConfigSelector:
       matchExpressions:
         - {key: machineconfiguration.openshift.io/role, operator: In, values: [layering, worker]}
     nodeSelector:
       matchLabels:
         node-role.kubernetes.io/layering: ""
   ```

## Injecting Custom Content

One of the benefits of using an on-cluster built OS image is the ability
to inject custom content into it. While the exact mechanism of how to do
this may evolve in the future, right now, one can use a Dockerfile to do
this. This step is completely optional and is not required to enable
on-cluster builds. If custom OS image content is desired, create the
`on-cluster-build-custom-dockerfile` ConfigMap in the MCO namespace, which
contains a 1:1 mapping of MachineConfigPool names to Dockerfile content:

```yaml
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: on-cluster-build-custom-dockerfile
  namespace: openshift-machine-config-operator
data:
  # This reflects a 1:1 mapping of MachineConfigPool name to custom Dockerfile.
  # In this example, empty Dockerfiles were provided to serve as an example
  # here. These are completely optional; one does not need to provide them at
  # all. 
  master: ""
  worker: ""
  layering: |-
    # Pull the centos base image and enable the EPEL repository.
    FROM quay.io/centos/centos:stream9 AS centos
    RUN dnf install -y epel-release

    # Pull an image containing the yq utility.
    FROM docker.io/mikefarah/yq:latest AS yq

    # Build the final OS image for this MachineConfigPool.
    FROM configs AS final

    # Copy the EPEL configs into the final image.
    COPY --from=yq /usr/bin/yq /usr/bin/yq
    COPY --from=centos /etc/yum.repos.d /etc/yum.repos.d
    COPY --from=centos /etc/pki/rpm-gpg/RPM-GPG-KEY-* /etc/pki/rpm-gpg/

    # Install cowsay and ripgrep from the EPEL repository into the final image,
    # along with a custom cow file.
    RUN sed -i 's/\$stream/9-stream/g' /etc/yum.repos.d/centos*.repo && \
        rpm-ostree install cowsay ripgrep && \
        curl -Lo /usr/share/cowsay/site-cows/rocko.cow 'https://raw.githubusercontent.com/paulkaefer/cowsay-files/main/cows/rocko.cow'
```

A couple of notes about the custom Dockerfile to be provided:

- Multiple build stages are allowed.

- One must use the configs image target (`FROM configs AS final`) to
  inject content into it and it must be the last image in the build.

- It is injected after the MachineConfigs are layered into the base OS
  image.

- If pulling additional images from a private container registry is
  desired, credentials must be included within the secret referred
  to in the `finalImagePullSecret` config key.

- Right now, updating the `on-cluster-build-custom-dockerfile` ConfigMap
  will not cause the image for that pool to be rebuilt. This limitation will
  be remedied in a future release.

## Building the image

1. Label the MachineConfigPool to opt it into layering:

   ```console
   $ oc label mcp/layering 'machineconfiguration.openshift.io/layering-enabled='`.
   ```

   It is not required for the MachineConfigPool to have nodes
   allocated to it; the build will happen regardless.

2. One should see a `machine-os-builder` pod start within the MCO
   namespace. Shortly afterward, one should also see a build pod
   within the MCO namespace for the layered MachineConfigPool.

3. Upon successful completion of the build process, the
   MachineConfigPool will have an annotation
   (`machineconfiguration.openshift.io/newestImageEquivalentConfig`)
   with the fully-qualified pullspec (e.g.,
   `quay.io/myorg/myrepo@sha256:324f17d18b197a951e7e11ea0b101836312191f92aefa0fc2ee240354cfbf7fc`).

## Rolling out the new image

1. Any nodes which are part of the opted-in MachineConfigPool will have
   the newly built image rolled out to them automatically after the
   build is complete. This will follow the same process and rules as
   MachineConfigs.

2. If the opted-in MachineConfigPool does not have any nodes associated
   with it, one can add nodes by adding a label to the node to
   indicate that it should use configs for that MachineConfigPool:

   ```console
   $ oc label node/node-name node-role.kubernetes.io/layering=
   ```

3. Nodes assigned to the opted-in MachineConfigPool
   will gain additional annotations,
   `machineconfiguration.openshift.io/desiredImage` and
   `machineconfiguration.openshift.io/currentImage`, respectfully.
   These annotations serve the same purpose as the
   `machineconfiguration.openshift.io/desiredConfig` and
   `machineconfiguration.openshift.io/currentConfig` annotations and
   are supplemental to those annotations. In other words, they do not replace
   the `machineconfiguration.openshift.io/desiredConfig` and
   `machineconfiguration.openshift.io/currentConfig` annotations.

4. The node will be cordoned, drained, and have the new config applied.
   Afterward, the node will reboot into the new configuration. One
   can verify this by running the following:

   ```console
   $ oc debug node/node-name
   Starting pod/node-name-debug ...
   To use host binaries, run `chroot /host`
   Pod IP: 10.0.29.42
   If you don't see a command prompt, try pressing enter.
   sh-4.4# chroot /host
   sh-5.1# rpm-ostree status
   State: idle
   Deployments:
   * ostree-unverified-registry:quay.io/myorg/myrepo@sha256:324f17d18b197a951e7e11ea0b101836312191f92aefa0fc2ee240354cfbf7
                   Digest: sha256:324f17d18b197a951e7e11ea0b101836312191f92aefa0fc2ee240354cfbf7
                   Version: 414.92.202308210204-0 (2023-08-23T12:21:02Z)
   ```

## Applying new configurations

1. Adding a new MachineConfig to a layered MachineConfigPool will cause
   a new image build to be performed.

2. After the new image is built, rolling out the new image will follow
   the same process as rolling out new MachineConfigs does. The
   difference is that the node will be rebased onto the new OS image
   which contains the new MachineConfigs instead of having the
   configs written directly to disk.

# Interacting with the MOB/on-cluster builds

## Viewing the Machine OS Builder logs

One can run the following command to view the Machine OS Builder pod logs, if
the MOB is currently running:

```console
$ oc logs -f "$(oc get pods -o name -l 'k8s-app=machine-os-builder' -n openshift-machine-config-operator)" -n openshift-machine-config-operator
```

## Viewing the OS image build logs

Depending on whether one is using the ImageBuildController or the
PodBuildController, one may view the build logs in one of two ways:

### PodBuildController

```console
$ oc logs -f -n openshift-machine-config-operator pod/build-<rendered MachineConfig name>
```

It may be necessary to specify which containers’ logs to view. For example, add
`-c image-build` to view the image build process or `-c wait-for-done` to view
the digestfile ConfigMap creation process.

### ImageBuildController

```console
$ oc logs -f -n openshift-machine-config-operator build/build-<rendered MachineConfig name>
```

## Viewing the rendered Dockerfile

Whenever a build is currently in progress, the rendered Dockerfile may
be viewed by running the following command:

```console
$ oc get configmap/dockerfile-<rendered MachineConfig name> -n openshift-machine-config-operator
```

However, if the build is successful, this ConfigMap will be deleted upon
completion of the build process since it is no longer needed.

## Viewing the rendered MachineConfig built into the final OS image

Whenever a build is currently in progress, the rendered MachineConfig
that is stored within the ConfigMap may be viewed by running:

```console
$ oc get configmap/mc-<rendered MachineConfig name> -n openshift-machine-config-operator -o json | jq -r '.data["machineconfig.json.gz"]' | base64 -d | gunzip | jq
```

However, if the build is successful, this ConfigMap will be deleted upon completion
of the build process since it is no longer needed.

## Cleaning up after a failed OS image build

The user experience around this is not great right now, so it is a point for
improvement.

Right now, the easiest way to do this is to remove the layering-enabled label
from the the MachineConfigPool, wait for the `machine-os-builder` to shut down,
then re-adding the label. This will allow the garbage collection processes built
into BuildController to remove the build pods, ConfigMaps, etc.

For this reason, when experimenting with a new custom Dockerfile, it is
recommended to create a new layered MachineConfigPool containing no nodes in
order to refine the build process. Once the build is successful, one can migrate
nodes into this new MachineConfigPool.

# Frequently Asked Questions

## What labels are added onto the final OS image?

Image labels indicating what MachineConfigPool this targets, the rendered
MachineConfig used to produce the configs, the base OS image pullspec, and
extensions OS image pullspec (when available) are added onto the final OS image.
This will enable future optimizations and enhancements by enabling a build to be
skipped whenever we already have an appropriate image built for that
MachineConfig.

# Known Limitations

- Rolling back from a layered-enabled MachineConfigPool is not currently
  supported in Dev Preview. If running in a cloud IPI environment such as AWS,
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

- Right now, we are not detecting whether the cluster has the OpenShift Image
  Builder capability. However in the future, the MOB process will determine
  whether the cluster has the OpenShift Image Builder capability before using the
  ImageBuildController and will default to using it if present. If so, it will
  start BuildController configured to use the ImageBuildController. If this
  capability does not exist, it will start BuildController configured to use the
  PodBuildController.

- There is a 1 MB constraint on MachineConfig size imposed by the fact
  that we temporarily store them in a ConfigMap. In the future, we
  could potentially make use of the Machine Config Server to pull
  the MachineConfig instead of storing it in a ConfigMap, which
  would eliminate the 1 MB constraint imposed by the current
  mechanism. While we could do some prefetching using an
  initContainer for builds managed by PodBuildController, this would
  not work when using ImageBuildController since it creates a Build
  object which does not offer this type of control. We could also
  use a Persistent Volume Claim (PVC), but that might not work in
  all OpenShift contexts and configurations.

- We do not currently ingest OS base images from the osImageURL field.

- Multi-arch is not currently supported, but will be in the future.

- Upgrades are currently untested, but will be supported in the future.

- Buildah version is not pinned or lifecycled with the rest of the
  OpenShift release for custom build pods. In other words, custom
  build pods are hard-coded to use `quay.io/buildah/stable:latest`.
  Because of this limitation, on-cluster builds will most likely not
  yet work in a disconnected environment.

- MachineConfig changes which require the extensions container (i.e.,
  enabling realtime kernels, et. al.) are not currently supported,
  but will be in the future.

- HyperShift is currently untested / unsupported, but will be in the
  future.
