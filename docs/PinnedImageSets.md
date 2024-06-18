# Updating a cluster in a disconnected environment without a local registry

You can use `PinnedImageSet` custom resources (CRs) to pin and pre-load release images to a defined machine config pool, which enables you to update an OpenShift Container Platform (OCP) cluster in a disconnected environment without needing an image registry in the environment.
This can be useful for clusters that were installed using the [OpenShift-based Appliance builder](https://access.redhat.com/articles/7065136), which might have been deployed in disconnected environments where building a local image registry is not practical.

**Warning**: The `PinnedImageSet` CR is a Technology Preview feature only. Technology Preview features are not supported with Red Hat production service level agreements (SLAs) and might not be functionally complete. Red Hat does not recommend using them in production. These features provide early access to upcoming product features, enabling customers to test functionality and provide feedback during the development process.
For more information about the support scope of Red Hat Technology Preview features, see [Technology Preview Features Support Scope](https://access.redhat.com/support/offerings/techpreview/).

**Note**: To update a cluster in a connected environment, or in a disconnected environment with a registry, see the product documentation for [updating a cluster](https://docs.openshift.com/container-platform/4.16/updating/understanding_updates/intro-to-updates.html).

## Prerequisites

* You have access to your cluster with administrator privileges.

## Procedure

1. Go to the [OpenShift Container Platform release repository](https://quay.io/repository/openshift-release-dev/ocp-release) and download the release images for your target update version onto a removable media, such as a USB drive.
**Tip**: You can find update recommendations and update paths on the [Red Hat OpenShift Container Platform Update Graph Application](https://access.redhat.com/labs/ocpupgradegraph/update_path).

2. Mount the removable media onto your cluster.

3. Create a `PinnedImageSet` CR for each machine config pool by running the following command:
```shell
$ oc create -f - << EOF
apiVersion: machineconfiguration.openshift.io/v1alpha1
kind: PinnedImageSet
metadata:
  labels:
    machineconfiguration.openshift.io/role: <mcp_label> (1)
  name: <pinned_image_set_name>
spec:
  pinnedImages:
  - name: "<release_images>" (2)
```
(1): Specify the machine config pool that the pinned images apply to. By default, `master` and `worker` pools are used.
(2): Specify the release images to pin to the machine config pool. For example, `”(need example here)”`.

4. (needs more info) Start the cluster update.