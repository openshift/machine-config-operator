# On-Cluster Layering Quickstart Guide

## Prerequisites
This quick-start guide assumes you have the following:
- Access to an OpenShift 4.19+ cluster.
- We will use an ImageStream for final image storage and retrieval. You can use an external image registry such as Quay.io, if desired.
- The OpenShift CLI tool (`oc`)
- [`yq`](https://github.com/mikefarah/yq)

## Background

### MachineOSConfig

The MachineOSConfig is the entrypoint into On-Cluster Layering (OCL). This is where you can specify what Containerfile to build, which MachineConfigPool to associate the build with, where the final image should be pushed and pulled from, as well as the secrets to use for those purposes. It's schema looks like this:

```yaml
---
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineOSConfig
metadata:
  name: layered
spec:
  # Here is where you refer to the MachineConfigPool that you want your built
  # image to be deployed to.
  machineConfigPool:
    name: layered
  containerFile:
  # Here is where you can set the Containerfile for your MachineConfigPool.
  - content: |-
      <containerfile contents>
  # Here is where you can select an image builder type. For now, we only
  # support the "Job" type that we maintain ourselves. Future integrations
  # can / will include other build system integrations.
  imageBuilder:
    imageBuilderType: Job
  # The Machine OS Builder needs to know what pull secret it can use to pull
  # the base OS image.
  baseImagePullSecret:
    name: <secret-name>
  # Here is where you specify the name of the push secret you use to push
  # your newly-built image to.
  renderedImagePushSecret:
    name: <secret-name>
  # Here is where you specify the image registry to push your newly-built
  # images to.
  renderedImagePushSpec: <final image pullspec>
```

There is a 1:1 relationship between a MachineOSConfig and a MachineConfigPool. It is possible to opt-in to only a single MachineConfigPool.

### MachineOSBuild

A `MachineOSBuild` instance represents a specific build associated with a MachineOSConfig. As a cluster admin, you will not need to worry about directly interacting with these for now. However, their presence can be used to determine what state a given build is in, such as whether it was successful, where to pull the image from, and what MachineConfig was built into the image.

## Getting Started

For the sake of this walk-through, we will create a MachineConfigPool called `layered` and we will associate a MachineOSConfig (also named `layered`) with this MachineConfigPool. Both the MachineConfigPool and the MachineOSConfig can be named anything one desires; however for the sake of this walk-through, we will use the name `layered`. We will also be using an ImageStream as our image registry although you are free to use an external image registry, if desired.

### Initial Setup

First, we'll create the necessary objects that we'll need in advance:

```bash
#!/usr/bin/env bash

# (required): Clone the global image pull secret into a secret called
# "global-pull-secret-copy" within the MCO namespace. This will be used to pull
# the base OS image:
oc create secret docker-registry global-pull-secret-copy \
  --namespace "openshift-machine-config-operator" \
  --from-file=.dockerconfigjson=<(oc get secret/pull-secret -n openshift-config -o go-template='{{index .data ".dockerconfigjson" | base64decode}}')

# (required for OCP): Clone the Red Hat entitlements certificate into the MCO
# namespace. This will be used to access RHEL content that you are entitled to
# access.
oc create secret generic etc-pki-entitlement \
  --namespace "openshift-machine-config-operator" \
  --from-file=entitlement.pem=<(oc get secret/etc-pki-entitlement -n openshift-config-managed -o go-template='{{index .data "entitlement.pem" | base64decode }}') \
  --from-file=entitlement-key.pem=<(oc get secret/etc-pki-entitlement -n openshift-config-managed -o go-template='{{index .data "entitlement-key.pem" | base64decode }}')

# (optional): Create an ImageStream which will act as our image registry. This
# step can be omitted if you are using an external image registry such as
# Quay.io, though you will need to create the push and pull secrets in Quay.io
# as well as inside your cluster.
oc create imagestream os-images -n openshift-machine-config-operator

# (optional): To get the image registry pullspec for your ImageStream, you can run the following command:
# This will return "image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-images"
oc get imagestream/os-images  -n openshift-machine-config-operator -o=jsonpath='{.status.dockerImageRepository}'

# (optional): Since this walk-through will use an ImageStream, we need to get the push and
# pull secret associated with the builder service account. For the sake of this
# demonstration, lets assume that this command returns the name
# "builder-dockercfg-".
#
# If using an external image registry, this step can be omitted, although you
# will still need to create the push and pull secrets in the MCO namespace.
oc get secrets -o name -n openshift-machine-config-operator -o=jsonpath='{.items[?(@.metadata.annotations.openshift\.io\/internal-registry-auth-token\.service-account=="builder")].metadata.name}'

# (required): Create the layered MachineConfigPool:
cat << EOF | oc create -f -
---
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfigPool
metadata:
  name: layered
spec:
  machineConfigSelector:
    matchExpressions:
      - key: machineconfiguration.openshift.io/role
        operator: In
        values:
        - worker
        - layered
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/layered: ""
EOF
```

### Customized OS Image

We will use the following Containerfile to install some useful packages on our cluster nodes:

```Dockerfile
FROM configs AS final
RUN rpm-ostree install tree && \
    ostree container commit
```

A couple of notes about this Containerfile:

- Multiple build stages are allowed.

- One must use the configs image target (`FROM configs AS final`) to
  inject content into it and it must be the last image in the build.

- It is injected after the MachineConfigs are layered into the base OS
  image.

- If pulling additional images from a private container registry is
  required, those pull credentials must be included within the secret referred
  to in the `baseImagePullSecret` config key.

### Create the MachineOSConfig

Finally, we create the MachineOSConfig object. For the sake of this walk-through, we will assume that the push / pull secret is named `builder-dockercfg-123` and we will assume that the name of the base image pull secret is `global-pull-secret-copy`. Although we will use [`yq`](https://github.com/mikefarah/yq) to create this file, you are welcome to use your favorite text editor instead:

```bash
#!/usr/bin/env bash

# Write the sample MachineOSConfig to a YAML file:
cat << EOF > layered-machineosconfig.yaml
---
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineOSConfig
metadata:
  name: layered
spec:
  # Here is where you refer to the MachineConfigPool that you want your built
  # image to be deployed to.
  machineConfigPool:
    name: layered
  containerFile:
  # Here is where you can set the Containerfile for your MachineConfigPool.
  # You'll note that you need to provide an architecture. This is because this
  # will eventually support multiarch clusters. For now, only noArch is
  # supported.
  - content: |-
      <containerfile contents>
  # Here is where you can select an image builder type. For now, we only
  # support the "Job" type that we maintain ourselves. Future integrations
  # can / will include other build system integrations.
  imageBuilder:
    imageBuilderType: Job
  # The Machine OS Builder needs to know what pull secret it can use to pull
  # the base OS image.
  baseImagePullSecret:
    name: <secret-name>
  # Here is where you specify the name of the push secret you use to push
  # your newly-built image to.
  renderedImagePushSecret:
    name: <secret-name>
  # Here is where you specify the image registry to push your newly-built
  # images to.
  renderedImagePushSpec: <final image pullspec>
EOF

# Write the Containerfile to a file:
cat << EOF > Containerfile
FROM configs AS final
RUN rpm-ostree install tree && \
    ostree container commit
EOF

# Finally, we'll modify our file using yq (https://github.com/mikefarah/yq).

# This is the name of the secret that will be used to push the built image to
# the image registry.
export pushSecretName="builder-dockercfg-123"

# This is the name of the secret that will be used to pull the built image from
# the image registry onto each node.
export pullSecretName="builder-dockercfg-123"

# This is the name of the secret that will be used to pull the base OS image to
# the build pod to be consumed during the build.
export baseImagePullSecretName="global-pull-secret-copy"

# This has the contents of the Containerfile.
export containerfileContents="$(cat Containerfile)"

# Notice that we added ":latest" onto our image registry pullspec. This tag will
# not actually be used.
export imageRegistryPullspec="image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-images:latest"


yq -i e '.spec.baseImagePullSecret.name = strenv(baseImagePullSecretName)' ./layered-machineosconfig.yaml
yq -i e '.spec.renderedImagePushSecret.name = strenv(pushSecretName)' ./layered-machineosconfig.yaml
yq -i e '.spec.containerFile[0].content = strenv(containerfileContents)' ./layered-machineosconfig.yaml
yq -i e '.spec.renderedImagePushSpec = strenv(imageRegistryPullspec)' ./layered-machineosconfig.yaml
```

This yields the following YAML:

```yaml
---
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineOSConfig
metadata:
  name: layered
spec:
  # Here is where you refer to the MachineConfigPool that you want your built
  # image to be deployed to.
  machineConfigPool:
    name: layered
  containerFile:
    # Here is where you can set the Containerfile for your MachineConfigPool.
    # You'll note that you need to provide an architecture. This is because
    # this will eventually support multiarch clusters.
    - content: |-
        FROM configs AS final
        RUN rpm-ostree install tree && \
          ostree container commit
  # Here is where you can select an image builder type. For now, we only
  # support a job image builder that we maintain ourselves. Future
  # integrations can / will include other build system integrations.
  imageBuilder:
    imageBuilderType: Job
  # The Machine OS Builder needs to know what pull secret it can use to pull
  # the base OS image.
  baseImagePullSecret:
    name: global-pull-secret-copy
  # Here is where you specify the name of the push secret you use to push
  # your newly-built image to.
  renderedImagePushSecret:
    name: builder-dockercfg-123
  # Here is where you specify the image registry to push your newly-built
  # images to. In this example, we're using an ImageStream, but one can
  # easily use an image registry such as Quay.io.
  renderedImagePushSpec: image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image:latest
```

Now that we've done this, we can apply the layered MachineOSConfig to our cluster:

```console
$ oc apply -f layered-machineosconfig.yaml
```

### Wait for the build to complete

The `machine-os-builder` pod starts which generates the `MachineOSBuild` object
and begins the build process. After the build completes, the MachineOSBuild and
MachineOSConfig will be updated with the digested image pullspec.

```console
$ oc get pods -n openshift-machine-config-operator

NAME                                                      READY   STATUS    RESTARTS       AGE
build-rendered-layered-de9c5e764b623c4065a1645261e9d553   2/2     Running   0              2s
machine-config-controller-569f7fc899-z29cz                2/2     Running   0              140m
machine-config-operator-5b687958f8-lmzlm                  2/2     Running   0              142m
machine-os-builder-68559b9c56-j9cgp                       1/1     Running   0              4s

(Other pods omitted for brevity)
```

We can also look at the MachineOSBuilds:

```console
$ oc get machineosbuilds

NAME                                       PREPARED   BUILDING   SUCCEEDED   INTERRUPTED   FAILED   AGE
layered-917498322fd0fa5e58671398b8cf7780   False      False      True        False         False    48m
```

### The build is complete

Now that the build has succeeded, we can take a closer look at the `MachineOSBuild` object:

```console
$ oc get machineosbuild/layered-rendered-layered-de9c5e764b623c4065a1645261e9d553-builder -o yaml

apiVersion: machineconfiguration.openshift.io/v1
kind: MachineOSBuild
metadata:
  annotations:
    machineconfiguration.openshift.io/job-uid: 6f0589cf-a34a-4880-ae7b-68fce1030e44
    machineconfiguration.openshift.io/rendered-image-push-secret: builder-dockercfg-123
  creationTimestamp: "2025-05-30T14:29:31Z"
  finalizers:
  - foregroundDeletion
  generation: 1
  labels:
    machineconfiguration.openshift.io/machine-os-config: layered
    machineconfiguration.openshift.io/rendered-machine-config: rendered-layered-6cd3c9b9ecb3b32dd93c403735fd9ad3
    machineconfiguration.openshift.io/target-machine-config-pool: layered
  name: layered-917498322fd0fa5e58671398b8cf7780
  ownerReferences:
  - apiVersion: machineconfiguration.openshift.io/v1
    blockOwnerDeletion: true
    controller: true
    kind: MachineOSConfig
    name: layered
    uid: 491ad6c9-e73c-4016-98b8-3da8ec1ce586
  resourceVersion: "109232"
  uid: 34ab137d-87d9-4a06-a493-c09cf117c100
spec:
  machineConfig:
    name: rendered-layered-6cd3c9b9ecb3b32dd93c403735fd9ad3
  machineOSConfig:
    name: layered
  renderedImagePushSpec: image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image:layered-917498322fd0fa5e58671398b8cf7780
status:
  buildEnd: "2025-05-30T14:34:51Z"
  buildStart: "2025-05-30T14:29:41Z"
  builder:
    imageBuilderType: Job
    job:
      group: batch
      name: build-layered-917498322fd0fa5e58671398b8cf7780
      namespace: openshift-machine-config-operator
      resource: jobs
  conditions:
  - lastTransitionTime: "2025-05-30T14:29:41Z"
    message: Build Failed
    reason: Failed
    status: "False"
    type: Failed
  - lastTransitionTime: "2025-05-30T14:29:41Z"
    message: Build Interrupted
    reason: Interrupted
    status: "False"
    type: Interrupted
  - lastTransitionTime: "2025-05-30T14:29:41Z"
    message: Build Prepared and Pending
    reason: Prepared
    status: "False"
    type: Prepared
  - lastTransitionTime: "2025-05-30T14:34:51Z"
    message: Image Build In Progress
    reason: Building
    status: "False"
    type: Building
  - lastTransitionTime: "2025-05-30T14:34:51Z"
    message: Build Ready
    reason: Ready
    status: "True"
    type: Succeeded
  digestedImagePushSpec: image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image@sha256:adf199658b8535c179fdb6e8ad40889b1b37d882a1bdb930d190d95b44bae548

```

We can see what MachineConfig the image was built with, the digested image pullspec, and its overall status. It is worth noting that although the `:latest` tag is shown above, all images will be tagged with the name of the MachineConfig they were built with (this is subject to change). Additionally, when they are pulled to each node, they are only pulled using a digested image pullspec.

### Rolling out the newly-built OS image

At this point, we now have a fully-built image, but we have not yet applied it
to any of our nodes. For the sake of this walk-through, we'll use the node named
`pabrodri-test-tr2jp-worker-a-4kn5h`:

```console
# First, lets look at what OS image our node is currently booted into:
$ oc debug node/pabrodri-test-tr2jp-worker-a-4kn5h-debug-wdsz7 -- chroot /host rpm-ostree status

Starting pod/pabrodri-test-tr2jp-worker-a-4kn5h-debug-wdsz7 ...
To use host binaries, run `chroot /host`
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.19-2025-05-28-210643@sha256:69af94fba046002fd10732d33ce2a7517779ef7d50b692a6cdeb7e779da112e7
                   Digest: sha256:69af94fba046002fd10732d33ce2a7517779ef7d50b692a6cdeb7e779da112e7
                  Version: 9.6.20250527-0 (2025-05-28T18:52:59Z)


Removing debug pod ...
```

We can see that we have the "factory" OS image that was provided by the current OpenShift release.

Next, we can ensure that the desired package is not installed on our node by doing something like this:

```console
$ oc debug node/pabrodri-test-tr2jp-worker-a-4kn5h -- chroot /host tree -a /var/home/core
Starting pod/pabrodri-test-tr2jp-worker-a-4kn5h-debug-s6hrp ...
To use host binaries, run `chroot /host`
chroot: failed to run command 'tree': No such file or directory

Removing debug pod ...
error: non-zero exit code from debug container
```

This confirms that we do not have the `tree` package on our node. Now, lets add the worker node to our `layered` MachineConfigPool by adding the `node-role.kubernetes.io/layered=` label to it so that it can use our newly-built OS image.

```console
$ oc label node/pabrodri-test-tr2jp-worker-a-4kn5h 'node-role.kubernetes.io/layered='
```

Now, we'll see the node drain and begin to rebase into the newly-built OS image. This will take a few minutes, but you can use this script that will wait for it to complete:

```bash
#!/usr/bin/env bash

nodeName="pabrodri-test-tr2jp-worker-a-4kn5h"

oc wait \
    --timeout=10m \
    --for=jsonpath='{.metadata.annotations.machineconfiguration\.openshift\.io/desiredImage=}{.metadata.annotations.machineconfiguration\.openshift\.io/currentImage}' \
    --for=jsonpath='{.metadata.annotations.machineconfiguration\.openshift\.io/state}=Done' \
    "node/$nodeName"
```

Once the node has finished the update, we can interrogate the node state like we did before:

```console
$ oc debug node/pabrodri-test-tr2jp-worker-a-4kn5h -- chroot /host rpm-ostree status

Starting pod/pabrodri-test-tr2jp-worker-a-4kn5h-debug-fvb22 ...
To use host binaries, run `chroot /host`
State: idle
Deployments:
* ostree-unverified-registry:image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image@sha256:adf199658b8535c179fdb6e8ad40889b1b37d882a1bdb930d190d95b44bae548
                   Digest: sha256:adf199658b8535c179fdb6e8ad40889b1b37d882a1bdb930d190d95b44bae548
                  Version: 9.6.20250527-0 (2025-05-30T14:33:49Z)


Removing debug pod ...
```

We can see now that our node has booted into our new OS image. But does it have `tree` installed?

```console
$  oc debug node/pabrodri-test-tr2jp-worker-a-4kn5h -- chroot /host tree -a /var/home/core
Starting pod/pabrodri-test-tr2jp-worker-a-4kn5h-debug-hltxn ...
To use host binaries, run `chroot /host`
/var/home/core
|-- .bash_logout
|-- .bash_profile
|-- .bashrc
`-- .ssh
    `-- authorized_keys.d
        `-- ignition

2 directories, 4 files

Removing debug pod ... 
```

Hooray! It does. It's also worth mentioning that we were able to use our RHEL entitlements to access packages which we're entitled to. In this example, the `tree` package came from the official RHEL 9 package repository.

## Conclusion

At this point, we now have a customized OS image installed on our cluster nodes. If the MachineConfigs for the `layered` MachineConfigPool are changed or the `Containerfile` is changed, a new `MachineOSBuild` will be created, the build will automatically start, and the image will be rolled out automatically to all of the nodes within the `layered` MachineConfigPool.

While this is a contrived example of installing a small helper tool, there are more useful use-cases such as installing device drivers, monitoring tools, etc.

## Further Reading
- [On-Cluster Layering Troubleshooting Guide](./onclusterlayering-troubleshooting.md)
- [RHCOS Layering Examples](https://github.com/openshift/rhcos-image-layering-examples)
- [CoreOS Layering Examples](ttps://github.com/coreos/layering-examples)