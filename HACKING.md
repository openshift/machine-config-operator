⚠⚠⚠ THIS IS A LIVING DOCUMENT AND LIKELY TO CHANGE QUICKLY ⚠⚠⚠

# Hacking on the MCD

1. Create a cluster (e.g. using [libvirt](https://github.com/openshift/installer/blob/master/Documentation/dev/libvirt-howto.md))
2. Delete the old node agent daemonset:

    ```sh
    oc delete ds tectonic-node-agent --namespace tectonic-system
    ```

    This will be fixed in the installer eventually.

3. Run:

    ```sh
    sed s/node-configuration.v1.coreos.com/machineconfiguration.openshift.io/g /opt/tectonic/node-annotations.json >/etc/machine-config-daemon/node-annotations.json
    ```

    The install process will take care of this eventually.

4. Create a namespace, e.g. `oc new-project mco`
5. Create items from `manifests/` in a sensible order, e.g.

    ```sh
    for f in manifests/*.crd.yaml; do oc create -f $f; done
    for f in manifests/*.machineconfigpool.yaml; do oc create -f $f; done
    oc create -f manifests/machineconfigdaemon/clusterrole.yaml
    oc create -f manifests/machineconfigdaemon/sa.yaml
    oc create -f manifests/machineconfigdaemon/scc.yaml
    oc create -f manifests/machineconfigdaemon/clusterrolebinding.yaml
    oc create -f manifests/machineconfigdaemon/daemonset.yaml
    ```

    (Though note you'll have to sub in `{{.TargetNamespace}}` with `mco`).
