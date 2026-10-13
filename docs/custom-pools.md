# Custom pools

Custom pools are pools that inherit from the `worker` pool. They use any MachineConfig targeted for the `worker` pool
but add the ability to deploy changes only targeted at the custom pool.
Since a custom pool inherits from the `worker` pool, any change to the `worker` pool is going to roll out to the custom pool as well (like an OS update during an upgrade).

**Note:** Custom pools on master nodes are not supported by MCO. For more detail, see [Custom pool on master node](#Custom-pool-on-master-node)

## Creating a custom pool

The first thing to do to create a custom pool is labeling the node with a custom role - in our example it will be `infra`:

```console
$ oc label node ip-10-0-130-218.us-west-1.compute.internal node-role.kubernetes.io/infra=
```

```console
$ oc get nodes
NAME                                         STATUS    ROLES          AGE   VERSION
ip-10-0-130-218.us-west-1.compute.internal   Ready     infra,worker   37m   v1.14.0+e020ea5b3
ip-10-0-131-9.us-west-1.compute.internal     Ready     master         43m   v1.14.0+e020ea5b3
ip-10-0-134-237.us-west-1.compute.internal   Ready     master         43m   v1.14.0+e020ea5b3
ip-10-0-138-167.us-west-1.compute.internal   Ready     worker         37m   v1.14.0+e020ea5b3
ip-10-0-151-146.us-west-1.compute.internal   Ready     master         43m   v1.14.0+e020ea5b3
ip-10-0-152-59.us-west-1.compute.internal    Ready     worker         37m   v1.14.0+e020ea5b3
```

When we create infra nodes, we apply them to existing worker nodes first. If you want to use the node as a purely infra node, you can remove the worker pool as follows:

```
oc label node ip-10-0-130-218.us-west-1.compute.internal node-role.kubernetes.io/worker-
```

This will not change the files on the node itself: the infra pool inherits from workers by default. This means that if you add a new MachineConfig to update workers, a purely infra node will still get updated. However this will mean that workloads scheduled for workers will no longer be scheduled on this node, as it no longer has the worker label.

Next you need to create a MachineConfigPool that contains both the `worker` role and your custom one as MachineConfig selector as follows:

```console
$ cat infra.mcp.yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfigPool
metadata:
  name: infra
spec:
  machineConfigSelector:
    matchExpressions:
      - {key: machineconfiguration.openshift.io/role, operator: In, values: [worker,infra]}
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/infra: ""
```

```console
$ oc create -f infra.mcp.yaml
```

You can check that the custom MCP has now been created:

```console
$ oc get mcp
NAME     CONFIG                                             UPDATED   UPDATING   DEGRADED
infra    rendered-infra-6db67f47c0b205c26561b1c5ab74d79b    True      False      False
master   rendered-master-7053d8fc3619388accc12c7759f8241a   True      False      False
worker   rendered-worker-6db67f47c0b205c26561b1c5ab74d79b   True      False      False
```

The example above makes an `infra` pool that contains all of the MachineConfigs used by the `worker` pool.

## Deploy changes to a custom pool (optional)

Deploying changes to a custom pool is just a matter of creating a MachineConfig that uses the custom pool name as the label (`infra` in the example):

```console
$ cat infra.mc.yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: infra
  name: 51-infra
spec:
  config:
    ignition:
      version: 2.2.0
    storage:
      files:
      - contents:
          source: data:,infra
        filesystem: root
        mode: 0644
        path: /etc/infratest
```

```console
$ oc create -f infra.mc.yaml
```

The custom `infra` pool has now deployed your change with the `infra`-targeted MachineConfig:

```console
$ oc get mcp
NAME     CONFIG                                             UPDATED   UPDATING   DEGRADED
infra    rendered-infra-dfdfdf7e006f18cd5d29cae03f77948b    True      False      False
master   rendered-master-7053d8fc3619388accc12c7759f8241a   True      False      False
worker   rendered-worker-6db67f47c0b205c26561b1c5ab74d79b   True      False      False
```

Now check that the file landed on the `infra` node:

```console
$ oc get pods -n openshift-machine-config-operator -l k8s-app=machine-config-daemon --field-selector "spec.nodeName=ip-10-0-130-218.us-west-1.compute.internal"
NAME                          READY   STATUS    RESTARTS   AGE
machine-config-daemon-vxb4c   1/1     Running   2          43m
```

```console
$ oc rsh -n openshift-machine-config-operator machine-config-daemon-vxb4c chroot /rootfs cat /etc/infratest
infra
```

## Removing a custom pool

Removing a custom pool requires first to un-label each node:

```console
$ oc label node ip-10-0-130-218.us-west-1.compute.internal node-role.kubernetes.io/infra-
```

Note: a node must have a role at any given time to be properly functioning. If you have infra-only nodes,
you should first relabel the node as worker and only then proceed to unlabel it from the infra role.

```console
$ oc get nodes
NAME                                         STATUS   ROLES    AGE   VERSION
ip-10-0-130-218.us-west-1.compute.internal   Ready    worker   50m   v1.14.0+e020ea5b3
ip-10-0-131-9.us-west-1.compute.internal     Ready    master   56m   v1.14.0+e020ea5b3
ip-10-0-134-237.us-west-1.compute.internal   Ready    master   56m   v1.14.0+e020ea5b3
ip-10-0-138-167.us-west-1.compute.internal   Ready    worker   50m   v1.14.0+e020ea5b3
ip-10-0-151-146.us-west-1.compute.internal   Ready    master   56m   v1.14.0+e020ea5b3
ip-10-0-152-59.us-west-1.compute.internal    Ready    worker   50m   v1.14.0+e020ea5b3
```

The MCO is then going to reconcile the node to the `worker` pool configuration.

```console
$ oc get mcp
NAME     CONFIG                                             UPDATED   UPDATING   DEGRADED
infra    rendered-infra-dfdfdf7e006f18cd5d29cae03f77948b    True      False      False
master   rendered-master-7053d8fc3619388accc12c7759f8241a   True      False      False
worker   rendered-worker-6db67f47c0b205c26561b1c5ab74d79b   False     True       False
```

As soon as the `worker` pool reconciles, you can remove the `infra` MCP and any MC created.

```console
$ oc get mcp
NAME     CONFIG                                             UPDATED   UPDATING   DEGRADED
infra    rendered-infra-dfdfdf7e006f18cd5d29cae03f77948b    True      False      False
master   rendered-master-7053d8fc3619388accc12c7759f8241a   True      False      False
worker   rendered-worker-6db67f47c0b205c26561b1c5ab74d79b   True      False      False

$ oc delete mc 51-infra
machineconfig.machineconfiguration.openshift.io "51-infra" deleted

$ oc delete mcp infra
machineconfigpool.machineconfiguration.openshift.io "infra" deleted
```

## Custom pool on master node
Custom pool on a node having master role is not supported. `oc label node` will apply the new custom role to the target master node but MCO will not apply changes specific to the custom pool. Error can be seen in the Machine Config Controller pod logs. This behaviour is to make sure that control plane nodes remain stable.

## Understanding custom pool updates

A node can be part of at most one pool.  The MCO will roll out updates for pools independently; for example, if there is an OS update or other change that affects all pools, normally 1 node from the `master` and `worker` pool would update at the same time.  If you add an `infra` pool for example, then 1 node from that pool will also try to roll out concurrently with the `master` and `worker`.
