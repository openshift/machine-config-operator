# Summary

Users need a way to set up mirror configuration. Users can set mirror configurations using ImageContentSourcePolicy via MCO currently, but if they want to use mirror when pulling image with tag instead of digest in the pull spec, they need to follow this [solution](https://access.redhat.com/solutions/4817401) to update the registries configuration via MCO. We can hopefully help customers by documenting CRDs with the `ImageDigestMirrorSet` and `ImageTagMirrorSet` tuneables and have the MCO use these tuneables when rendering the registries.conf files to Ignition/disk.

## Goals

1. Setting cluster wide mirror registries via `ImageDigestMirrorSet`. The mirrors will be used only when the pull spec is specified with a digest.

2. Setting cluster wide mirror registries via `ImageTagMirrorSet`. The mirrors will be used only when the pull spec is specified with a tag.

3. Setting cluster wide mirror registries, but blocking the image pull from the registry that specified as source in the pull spec by setting the enum field `mirrorSourcePolicy`. If all the mirror registries are failed, the image pull will be redirected to the registry in the pull spec by default. This field is available for both `ImageDigestMirrorSet` and `ImageTagMirrorSet`.

4.  Before the `ImageContentSourcePolicy` CRD gets deprecated, reject to update one of `ImageDigestMirrorSet`/ `ImageTagMirrorSet` and `ImageContentSourcePolicy` when there is an attempt to update both objects on the cluster.

## Non-Goals

Using by-tag configurations for OpenShift release images is not recommended. Those should still be referenced by digest.

# Proposal

Extend the ContainerRuntimeConfigController to support ImageDigestMirrorSet and ImageTagMirrorSet CRDs. Adding or deleting `ImageDigestMirrorSet` or `ImageTagMirrorSet` objects will add or delete the corresponding registry entries of registries.conf, see **[containers-registries.conf(5)](https://github.com/containers/image/blob/main/docs/containers-registries.conf.5.md)** for more information.

## CRD

[ImageDigestMirrorSet CRD](https://github.com/openshift/api/blob/master/config/v1/0000_10_config-operator_01_imagedigestmirrorset.crd.yaml)

[ImageTagMirrorSet CRD](https://github.com/openshift/api/blob/master/config/v1/0000_10_config-operator_01_imagetagmirrorset.crd.yaml)


## Validation and Troubleshooting

The above CRDs have kubebuilder annotations to validate the fields syntax.

ContainerRuntimeConfigController validates the conflicting contents of the resources, setting up different `mirrorSourcePolicy` for the same source registry, the source registry is one of the mirror registry but it is blocked, etc.

The syntax error will fail at the api server. For other errors like resources contents error, users need to look into the logs of the `machine-config-controller` container to get more information.

e.g.

The ImageDigestMirrorSet has mirror filed violates the regex:

```bash
$ oc create -f digest-mirror.yaml
The ImageDigestMirrorSet "digest-mirror" is invalid: spec.imageDigestMirrors[0].mirrors[1]: Invalid value: "": spec.imageDigestMirrors[0].mirrors[1] in body should match '^((?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])(?:(?:\.(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9]))+)?(?::[0-9]+)?)(?:(?:/[a-z0-9]+(?:(?:(?:[._]|__|[-]*)[a-z0-9]+)+)?)+)?$'
```

The ImageDigestMirrorSet has conflicting value:

```bash
I0131 02:52:30.341064       1 container_runtime_config_controller.go:860] Applied ImageConfig cluster on MachineConfigPool worker
I0131 02:52:35.247702       1 render_controller.go:569] BaseOSContainerImage=registry.build05.ci.openshift.org/ci-ln-69jp7nt/stable@sha256:267e836daba5d8ebfa1356b7dd87e6568efb0f1e474440895efe03dea24e0073
I0131 02:52:35.294641       1 render_controller.go:536] Pool master: now targeting: rendered-master-9b430aec329979c64c025210a449a571
I0131 02:52:35.342140       1 render_controller.go:569] BaseOSContainerImage=registry.build05.ci.openshift.org/ci-ln-69jp7nt/stable@sha256:267e836daba5d8ebfa1356b7dd87e6568efb0f1e474440895efe03dea24e0073
I0131 02:52:35.384977       1 render_controller.go:536] Pool worker: now targeting: rendered-worker-3cf94f40c15b92dc464cff39d0d20f4a
I0131 02:52:36.080612       1 container_runtime_config_controller.go:415] Error syncing image config openshift-config: could not Create/Update MachineConfig: could not update registries config with new changes: cannot set mirrorSourcePolicy: NeverContactSource if the source "registry.access.redhat.com/ubi8/ubi-minimal" is one of the mirrors
```

## Example

### ImageDigestMirrorSet sets mirrors for digest pull spec

This is what an example `ImageDigestMirrorSet` CR looks like. 
This limits the usage of mirror only for digest pull spec.

```yml
apiVersion: config.openshift.io/v1
kind: ImageDigestMirrorSet
metadata:
  name: digest-mirror
spec:
  imageDigestMirrors:
  - mirrors:
    - example.io/digest-example/ubi-minimal 
    source: registry.access.redhat.com/ubi8/ubi-minimal
    mirrorSourcePolicy: NeverContactSource  # do not redirect to the source registry if the pull from the mirror is failed
```
Save your `ImageDigestMirrorSet` locally, for example as digest-mirror.yaml


Apply the `ImageDigestMirrorSet` that you created:


```
oc create -f digest-mirror.yaml
```

Check that it was created:

```
$ oc get imagedigestmirrorset
NAME             AGE
digest-mirror   50s
```

You can verify the `digest-mirror` is rolled out to each node by checking /etc/containers/registries.conf on the node.

```
$ oc debug nodes/ovirt17-4qmd6-worker-xnxgp
Starting pod/ovirt17-4qmd6-worker-xnxgp-debug ...
To use host binaries, run `chroot /host`
Pod IP: 192.168.217.136
If you don't see a command prompt, try pressing enter.
sh-4.4# chroot /host
sh-4.4# cat /etc/containers/registries.conf
unqualified-search-registries = ["registry.access.redhat.com", "docker.io"]
short-name-mode = ""

[[registry]]
  prefix = ""
  location = "registry.access.redhat.com/ubi8/ubi-minimal"
  blocked = true

  [[registry.mirror]]
    location = "example.io/digest-example/ubi-minimal"
    pull-from-mirror = "digest-only"
```

### ImageTagMirrorSet sets mirrors for tag pull spec

This is what an example `ImageTagMirrorSet` CR looks like. 
This limits the usage of mirror only for tag pull spec.

```yml
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
  name: tag-mirror
spec:
  imageTagMirrors:
  - mirrors:
    - mirror.example.com/redhat
    source: registry.redhat.io/openshift4
    mirrorSourcePolicy: AllowContactingSource
```
Apply the `ImageTagMirrorSet` and verify the registries.conf use the above steps. Note that the `pull-from-mirror` is `tag-only`. 

```
[[registry]]
  prefix = ""
  location = "registry.redhat.io/openshift4"

  [[registry.mirror]]
    location = "mirror.example.com/redhat"
    pull-from-mirror = "tag-only"
```

## Implementation Details

The ContainerRuntimeConfigController would perform the following steps:

1. Validate the ImageDigestMirrorSet and ImageTagMirrorSet objects on the cluster

2. Render the current MachineConfigs (storage.files.contents[registries.conf]) into the originalRegistriesIgn

3. Serialize all the existing ImageDigestMirrorSet and ImageTagMirrorSet to the registries.conf toml file

4. Update the ignition file /etc/containers/registries.conf within the 99-[role]-generated-registries MachineConfig

After deletion of the ImageDigestMirrorSet or the ImageTagMirrorSet instance the config will be reverted to the original registries config.

## See Also
**[containers-registries.conf(5)](https://github.com/containers/image/blob/main/docs/containers-registries.conf.5.md)**

