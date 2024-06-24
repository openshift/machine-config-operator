# NodeDisruptionPolicy

Node Disruption Policy allows the user to define to what actions to take for minor MachineConfig updates. The MCO is introducing this feature in TechPreview for 4.16. More details about the design can be found in the [enhancement](https://github.com/openshift/enhancements/pull/1525).

## Goals

* Have users be able to define no-action and service reloads to specific MachineConfig changes
* Have users be able to easily see existing cluster non-disruptive update cases

## Non-Goals

* Have NodeDisruptionPolicy apply to non-MCO driven changes (e.g. SRIOV can still reboot nodes)
* Remove existing non-disruptive update paths (the user will be able to override cluster defaults)
* Design for image-based updates (live apply and bootc, will be considered in the future)
* Have the MCO validate whether a change can be successfully applied with the given NodeDisruptionPolicy (i.e. it is up to the responsibility of the user to ensure the correctness of their defined actions)

## How it works right now

The current cluster policies are always listed in NodeDisruptionPolicyStatus in a `MachineConfiguration`(not to be confused with `MachineConfig`) CR, called "cluster". All configurations should be applied to the `spec.nodeDisruptionPolicy` field of this CR. The operator will merge the user defined policies and the cluster defaults and then display them in the `status.nodeDisruptionPolicyStatus` field.

**Note: All configuration must be done to the CR named "cluster". Any `MachineConfiguration` object made with another name will be rejected via a ValidatingAdmissionPolicy. This will be the single point of control for all Node Disruption Policies.**

For example, when there is no user defined policies, the status will just show the cluster defaults. See below:

```console
$ oc get MachineConfiguration/cluster -o yaml
apiVersion: operator.openshift.io/v1
kind: MachineConfiguration
metadata:
  creationTimestamp: "2024-04-16T15:02:37Z"
  generation: 4
  name: cluster
  resourceVersion: "261205"
  uid: 2c67b155-1898-452f-adbd-ed376afc0ea2
spec:
  logLevel: Normal
  managementState: Managed
  operatorLogLevel: Normal
status:
  nodeDisruptionPolicyStatus:
    clusterPolicies:
      files:
      - actions:
        - type: None
        path: /var/lib/kubelet/config.json
      - actions:
        - reload:
            serviceName: crio.service
          type: Reload
        path: /etc/machine-config-daemon/no-reboot/containers-gpg.pub
      - actions:
        - reload:
            serviceName: crio.service
          type: Reload
        path: /etc/containers/policy.json
      - actions:
        - type: Special
        path: /etc/containers/registries.conf
      sshkey:
        actions:
        - type: None
  readyReplicas: 0
```
Say, for instance the user applied the following MachineConfiguration:
```
apiVersion: operator.openshift.io/v1
kind: MachineConfiguration
metadata:
  name: cluster
  namespace: openshift-machine-config-operator
spec:
  nodeDisruptionPolicy:
    files:
      - path: /etc/my-file
        actions:
          - type: None
```
Now, the Status will be updated to reflect the merged policy:
```
$ oc get MachineConfiguration/cluster -o yaml
apiVersion: operator.openshift.io/v1
kind: MachineConfiguration
metadata:
  creationTimestamp: "2024-04-16T15:02:37Z"
  generation: 4
  name: cluster
  resourceVersion: "261205"
  uid: 2c67b155-1898-452f-adbd-ed376afc0ea2
spec:
  nodeDisruptionPolicy:
    files:
      - path: /etc/my-file
        actions:
          - type: None
  logLevel: Normal
  managementState: Managed
  operatorLogLevel: Normal
status:
  nodeDisruptionPolicyStatus:
    clusterPolicies:
      files:
      - actions:
        - type: None
        path: /etc/my-file
      - actions:
        - type: None
        path: /var/lib/kubelet/config.json
      - actions:
        - reload:
            serviceName: crio.service
          type: Reload
        path: /etc/machine-config-daemon/no-reboot/containers-gpg.pub
      - actions:
        - reload:
            serviceName: crio.service
          type: Reload
        path: /etc/containers/policy.json
      - actions:
        - type: Special
        path: /etc/containers/registries.conf
      sshkey:
        actions:
        - type: None
  readyReplicas: 0

```


For this initial implementation the policy supports MachineConfig changes to the following:
- Files
- Units
- sshKeys

The following actions are supported:
- `None`: No action will be done for this MachineConfig change.
- `Drain`: This will drain the node of its current workload.
- `Reload`: Reloads a user specified service. This requires specifying the service to be reloaded in the `reload.serviceName` field.
- `Restart`: Restarts a user specified service. This requires specifying the service to be restarted in the `restart.serviceName` field
- `DaemonReload`: This executes a daemon-reload, which reloads the systemd manager configuration.
- `Reboot`: This will reboot the node.
- `Special`: This is an internal MCO only action and cannot be set by the user.

## Some key points to note

- The default action for an unspecified change is reboot.
- If there is a conflict between a user defined policy and the cluster default, the user defined policy will override the cluster default.
- If any of the changes result in a reboot action, all other policies will be ignored.
- There is no dedup of the final actions list. It is possible an action may be repeated if multiple policies are in effect for MachineConfig change.
- It is important to remember that the cluster node disruption policy (as defined by the status of `MachineConfiguration/cluster`) applies to the difference between currentConfig and desiredConfig. If a file/service is added, updated or removed by a new MachineConfig change, then the node disruption policy for that respective file/service will be in effect. If no policy is defined for this change, it will result in a Reboot action.
