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

3. Set `KUBECONFIG` for use with `oc` 

   ```sh
   export KUBECONFIG=<path to kubeconfig>
   ```

4. Configure the MCO to deploy your test version. There is a ConfigMap in the
   `openshift-machine-config-operator` namespace that contains the versions of
   the components that the operator will deploy. When modified, the operator
   will automatically deploy the new container versions.

   Note that for newer clusters set up by the installer, you must first disable
   CVO as it owns the configmap and it will revert your changes:

   ```sh
   oc delete deploy cluster-version-operator -n openshift-cluster-version
   ```

   then change the "MachineConfigDaemon" value in the images.json field to your container image, e.g. "docker.io/sdemos/origin-machine-config-daemon:latest" for the previous example:

   ```sh
   oc edit configmap -n openshift-machine-config-operator machine-config-operator-images
   ```

5. Check that the deployment was successful. Open the yaml file and confirm that new image location (docker.io/...)
   is present (check field-> spec: template: spec: image:)
 
   ```sh
   oc get -n openshift-machine-config-operator daemonset machine-config-daemon -o yaml
   ```

# How to lay down files with the MCD

1. Create a new machineconfig:


    ```yaml
    # test.yaml
    apiVersion: machineconfiguration.openshift.io/v1
    kind: MachineConfig
    metadata:
      labels:
        machineconfiguration.openshift.io/role: worker
      name: test-file
    spec:
      config:
        storage:
          files:
          - contents:
              source: data:,hello%20world%0A
              verification: {}
            filesystem: root
            mode: 420
            path: /home/core/test
    ```

    Then:

    ```sh
    oc create -f test.yaml
    ```

1. The MCC will then notice this, generate a new merged
   MachineConfig and update the node annotation for the
   worker. You can monitor new MachineConfig objects with:

   ```sh
   oc get machineconfigs --watch
   ```

   You can monitor the MCD logs on a worker to see when it
   reacts to the node annotation change and reboots the
   system:

   ```sh
   oc logs -f machine-config-daemon-<hash>
   ```
