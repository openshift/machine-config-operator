⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCD

1. Create a cluster using [the installer](https://github.com/openshift/installer/).  Many of the MCD developers use libvirt.  These instructions will be kept up to date generally against the leading edge of the installer.

1. Build a container image for the MCD and push it to a registry somewhere, e.g.

   ```sh
   # this takes care of building the binary for you as well
   WHAT=machine-config-daemon ./hack/build-image.sh
   WHAT=machine-config-daemon REPO=docker.io/sdemos ./hack/push-image.sh
   ```

1. Set `KUBECONFIG` for use with `oc`

   ```sh
   export KUBECONFIG=<path to kubeconfig>
   ```

1. (optional) Since most of your work will be in the `openshift-machine-config-operator` namespace, you may find it convenient to:

   ```sh
   oc project openshift-machine-config-operator
   ```

   Then you can omit `-n openshift-machine-config-operator` to most commands.

1. Configure the MCO to deploy your test version. There is a ConfigMap in the
   `openshift-machine-config-operator` namespace that contains the versions of
   the components that the operator will deploy. When modified, the operator
   will automatically deploy the new container versions.

   Note that for newer clusters set up by the installer, you must first disable
   CVO as it owns the configmap and it will revert your changes:

   ```sh
   oc -n openshift-cluster-version scale --replicas=0 deploy/cluster-version-operator
   ```

   (If you later want the CVO back to do cluster upgrades, use `--replicas=1` to restore it)

   To use your new container, change the "MachineConfigDaemon" value in the images.json field to your container image, e.g. "docker.io/sdemos/origin-machine-config-daemon:latest" for the previous example:

   ```sh
   oc edit configmap -n openshift-machine-config-operator machine-config-operator-images
   ```

1. Check that the deployment was successful. Open the yaml file and confirm that new image location (docker.io/...)
   is present (check field-> spec: template: spec: image:)
 
   ```sh
   oc get -n openshift-machine-config-operator daemonset machine-config-daemon -o yaml
   ```
