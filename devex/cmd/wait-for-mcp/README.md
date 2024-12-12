# wait-for-mcp

This is a small utility that will wait for a given MachineConfigPool to complete its rollout.

## Usage:

One can specify which MachineConfigPool(s) to watch in a given cluster:

```console
$ wait-for-mcp worker
I0921 10:58:59.331211   43576 main.go:36] Timeout set to 15m0s
I0921 10:58:59.421173   43576 machineconfigpool.go:103] Current nodes for pool "worker" are: [ip-10-0-4-22.ec2.internal ip-10-0-54-17.ec2.internal]
I0921 10:58:59.421207   43576 machineconfigpool.go:104] Waiting for nodes in pool to reach MachineConfig rendered-worker-1033b215f4eb45fc49be53483af65cb2
I0921 10:58:59.452833   43576 machineconfigpool.go:136] Node ip-10-0-4-22.ec2.internal in pool worker has completed its update after 31.671925ms. 1 node(s) remaining: [ip-10-0-54-17.ec2.internal]
I0921 10:58:59.452872   43576 machineconfigpool.go:136] Node ip-10-0-54-17.ec2.internal in pool worker has completed its update after 31.720111ms. 0 node(s) remaining: []
I0921 10:58:59.452884   43576 machineconfigpool.go:142] 2 nodes in pool worker have completed their update after 31.732079ms
```

One can watch multiple MachineConfigPools by adding them as space-separated arguments:

```console
$ wait-for-mcp worker master
I0921 10:59:41.715889   45476 main.go:36] Timeout set to 15m0s
I0921 10:59:45.001080   45476 machineconfigpool.go:103] Current nodes for pool "master" are: [ip-10-0-1-118.ec2.internal ip-10-0-11-70.ec2.internal ip-10-0-6-93.ec2.internal]
I0921 10:59:45.001144   45476 machineconfigpool.go:104] Waiting for nodes in pool to reach MachineConfig rendered-master-0a15368521cd8c2b6eb4688e52019ad5
I0921 10:59:45.007048   45476 machineconfigpool.go:103] Current nodes for pool "worker" are: [ip-10-0-4-22.ec2.internal ip-10-0-54-17.ec2.internal]
I0921 10:59:45.007069   45476 machineconfigpool.go:104] Waiting for nodes in pool to reach MachineConfig rendered-worker-1033b215f4eb45fc49be53483af65cb2
I0921 10:59:45.044355   45476 machineconfigpool.go:136] Node ip-10-0-1-118.ec2.internal in pool master has completed its update after 43.299948ms. 2 node(s) remaining: [ip-10-0-11-70.ec2.internal ip-10-0-6-93.ec2.internal]
I0921 10:59:45.044386   45476 machineconfigpool.go:136] Node ip-10-0-11-70.ec2.internal in pool master has completed its update after 43.336307ms. 1 node(s) remaining: [ip-10-0-6-93.ec2.internal]
I0921 10:59:45.044396   45476 machineconfigpool.go:136] Node ip-10-0-6-93.ec2.internal in pool master has completed its update after 43.346557ms. 0 node(s) remaining: []
I0921 10:59:45.044403   45476 machineconfigpool.go:142] 3 nodes in pool master have completed their update after 43.353696ms
I0921 10:59:45.045320   45476 machineconfigpool.go:136] Node ip-10-0-4-22.ec2.internal in pool worker has completed its update after 38.271006ms. 1 node(s) remaining: [ip-10-0-54-17.ec2.internal]
I0921 10:59:45.045342   45476 machineconfigpool.go:136] Node ip-10-0-54-17.ec2.internal in pool worker has completed its update after 38.296644ms. 0 node(s) remaining: []
I0921 10:59:45.045351   45476 machineconfigpool.go:142] 2 nodes in pool worker have completed their update after 38.305848ms

```

Alternatively, if no MachineConfigPools are provided, `wait-for-mcp` will watch
all the MachineConfigPools in a given cluster. Additionaly, there is a
`--timeout` flag which can be used to set a custom timeout. The default timeout
is 15 minutes.
