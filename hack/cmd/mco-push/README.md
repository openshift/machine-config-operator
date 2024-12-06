# mco-push

This utility can be used to replace the MCO image in a running cluster with the provided image pullspec.

```console
$ mco-push --help
Automates the replacement of the machine-config-operator (MCO) image in an OpenShift cluster for testing purposes.

Usage:
  mco-push [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  replace     Replaces the MCO image with the provided container image pullspec
  restart     Restarts all of the MCO pods
  revert      Reverts the MCO image to the one in the OpenShift release
  version     Print the current version

Flags:
  -h, --help                           help for mco-push
      --kubeconfig string              Paths to a kubeconfig. Only required if out-of-cluster.
      --log-flush-frequency duration   Maximum number of seconds between log flushes (default 5s)
  -v, --v Level                        number for the log level verbosity
      --vmodule moduleSpec             comma-separated list of pattern=N settings for file-filtered logging (only works for the default text log format)

Use "mco-push [command] --help" for more information about a command.
```

## Usage

### Replace

To replace the MCO container image, one can do the following:

```console
$ mco replace quay.io/org/repo:latest
I0425 15:24:25.401465 1153643 rollout.go:360] Setting replicas for openshift-cluster-version/cluster-version-operator to 0
I0425 15:24:25.490284 1153643 rollout.go:360] Setting replicas for openshift-machine-config-operator/machine-config-operator to 0
W0425 15:24:25.539731 1153643 rollout.go:249] ConfigMap machine-config-operator-images has pullspec quay.io/org/repo:original-image, which will change to quay.io/org/repo:latest. A MachineConfig update will occur as a result.
I0425 15:24:25.551096 1153643 rollout.go:300] Updating deployment/machine-config-operator
I0425 15:24:25.551120 1153643 rollout.go:300] Updating deployment/machine-config-controller
I0425 15:24:25.552020 1153643 rollout.go:321] Updating daemonset/machine-config-server
I0425 15:24:25.552411 1153643 rollout.go:321] Updating daemonset/machine-config-daemon
I0425 15:24:25.577878 1153643 rollout.go:281] Set machineConfigOperator in images.json in ConfigMap machine-config-operator-images to quay.io/org/repo:latest
I0425 15:24:26.216581 1153643 rollout.go:300] Updating deployment/machine-config-operator
I0425 15:24:26.424259 1153643 rollout.go:360] Setting replicas for openshift-machine-config-operator/machine-config-operator to 1
I0425 15:24:26.821919 1153643 replace.go:61] Successfully replaced the stock MCO image with quay.io/org/repo:latest.
```

This will perform the following operations:

1. Scale down the `cluster-version-operator`.
2. Temporarily scale down the `machine-config-operator`.
3. Update the MCO deployments and daemonsets.
4. Replace pullspec under the `machineConfigOperator` key in the `machine-config-operator-images` ConfigMap.
5. Scale the `machine-config-operator` back up.

One should use a tagged image pullspec instead of a digested pullspec for this
because it will incur a MachineConfig update which will roll out to all of the
nodes on the cluster. When a tagged pullspec is used, this MachineConfig update
will only occur the first time. Future replacements with the same tagged
pullspec will not incur a MachineConfig update.

### Revert

To replace the overridden MCO image with the stock image, one can do the following:

```console
$ mco revert
I0425 15:32:50.985415 1183777 rollout.go:58] Found original MCO image quay.io/org/repo:original-tag for the currently running cluster release (quay.io/org/repo@sha256:78864229b34c78744f15eb6af0824c5f7a88ae70a90bfd1bd77aff7e8f3c3965)
I0425 15:32:50.985455 1183777 rollout.go:360] Setting replicas for openshift-cluster-version/cluster-version-operator to 0
I0425 15:32:51.014501 1183777 rollout.go:360] Setting replicas for openshift-machine-config-operator/machine-config-operator to 0
I0425 15:32:51.070697 1183777 rollout.go:300] Updating deployment/machine-os-builder
W0425 15:32:51.071006 1183777 rollout.go:249] ConfigMap machine-config-operator-images has pullspec quay.io/org/repo:latest, which will change to quay.io/org/repo:original-tag. A MachineConfig update will occur as a result.
I0425 15:32:51.083054 1183777 rollout.go:300] Updating deployment/machine-config-operator
I0425 15:32:51.083075 1183777 rollout.go:300] Updating deployment/machine-config-controller
I0425 15:32:51.083386 1183777 rollout.go:321] Updating daemonset/machine-config-server
I0425 15:32:51.083426 1183777 rollout.go:321] Updating daemonset/machine-config-daemon
I0425 15:32:51.117268 1183777 rollout.go:281] Set machineConfigOperator in images.json in ConfigMap machine-config-operator-images to quay.io/org/repo:original-tag
I0425 15:32:52.004867 1183777 rollout.go:300] Updating deployment/machine-config-operator
I0425 15:32:52.214458 1183777 rollout.go:360] Setting replicas for openshift-machine-config-operator/machine-config-operator to 1
I0425 15:32:52.614863 1183777 rollout.go:360] Setting replicas for openshift-cluster-version/cluster-version-operator to 1
I0425 15:32:53.010042 1183777 revert.go:36] Successfully rolled back to the original MCO image
```

This will look what the stock image should be, perform the above steps, and restore the `cluster-version-operator`'s replicas.

### Restart

```console
$ mco restart
mco-push restart
I0425 15:35:07.693697 1191976 rollout.go:360] Setting replicas for openshift-cluster-version/cluster-version-operator to 0
I0425 15:35:07.734319 1191976 rollout.go:360] Setting replicas for openshift-machine-config-operator/machine-config-operator to 0
I0425 15:35:07.809768 1191976 rollout.go:304] Restarting deployment/machine-config-operator
I0425 15:35:07.820955 1191976 rollout.go:254] ConfigMap machine-config-operator-images already has pullspec quay.io/org/repo:latest. Will restart MCO components to cause an update.
I0425 15:35:07.821322 1191976 rollout.go:304] Restarting deployment/machine-os-builder
I0425 15:35:07.821446 1191976 rollout.go:325] Restarting daemonset/machine-config-server
I0425 15:35:07.821717 1191976 rollout.go:325] Restarting daemonset/machine-config-daemon
I0425 15:35:07.821741 1191976 rollout.go:304] Restarting deployment/machine-config-controller
I0425 15:35:08.711907 1191976 rollout.go:304] Restarting deployment/machine-config-operator
I0425 15:35:08.922522 1191976 rollout.go:360] Setting replicas for openshift-machine-config-operator/machine-config-operator to 1
```

This will restart the MCO's deployments and daemonsets. There is an optional
`--force` flag that one can use that will delete the pods corresponding to
those deployments and daemonsets to force a reboot faster.
