⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCD

These instructions were tested with installer version https://github.com/openshift/installer/commit/d0b3f913981ef79fd58f089909d7ef1918aa3894 and RHCOS version 4.0.5836.

1. Create a cluster (e.g. using [libvirt](https://github.com/openshift/installer/blob/d0b3f913981ef79fd58f089909d7ef1918aa3894/Documentation/dev/libvirt-howto.md))
2. Build a container image for the MCD and push it to a registry somewhere, e.g.

   ```sh
   # this takes care of building the binary for you as well
   WHAT=machine-config-daemon ./hack/build-image.sh
   WHAT=machine-config-daemon REPO=docker.io/sdemos ./hack/push-image.sh
   ```

3. Configure the MCO to deploy your test version. There is a ConfigMap in the
   `openshift-machine-config-operator` namespace that contains the versions of
   the components that the operator will deploy. When modified, the operator
   will automatically deploy the new container versions.

   Note that for newer clusters set up by the installer, you must first disable
   CVO as it owns the configmap and it will revert your changes:

   ```sh
   # if using kubectl:
   kubectl delete ds cluster-version-operator -n openshift-cluster-version

   # if using oc:
   oc delete daemonset cluster-version-operator -n openshift-cluster-version --config=auth/kubeconfig
   ```

   then change the "MachineConfigDaemon" value in the images.json field to your container image, e.g. "docker.io/sdemos/origin-machine-config-daemon:latest" for the previous example:

   ```sh
   # if using kubectl:
   kubectl edit configmap -n openshift-machine-config-operator machine-config-operator-images

   # if using oc:
   oc edit configmap -n openshift-machine-config-operator machine-config-operator-images --config=auth/kubeconfig
   ```

4. Check that the deployment was successful. Open the yaml file and confirm that new image location (docker.io/...)
   is present (check field-> spec: template: spec: image:)
 
   ```sh
   #if using kubectl:
   kubectl get -n openshift-machine-config-operator ds machine-config-daemon -o yaml

   #if using oc:
   oc get -n openshift-machine-config-operator daemonset machine-config-daemon -o yaml --config=auth/kubeconfig
   ```