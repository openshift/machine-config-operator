# apiserver-watcher

## Background

Some cloud provider load balancers need special handling for hairpin scenarios.
Because default OpenShift installations are "self-driving", i.e. the control 
plane is hosted as part of the cluster, we rely on hairpin extensively.


```
 +---------------+
 |               |          +-----------------+
 |  +---------+  |          |                 |
 |  | kubelet +------------->  layer-3        |
 |  +---------+  |          |  load balancer  |
 |               |     +----+                 |
 |  +---------+  |     |    +-----------------+
 |  |apiserver+<-------+
 |  +---------+  |
 |               |
 +---------------+
```

We have iptables workarounds to fix these scenarios, but they need to know when
the local apiserver is up or down. Hence, the apiserver-watcher.

### GCP

Google cloud load balancer is a L3LB that is special. It doesn't do DNAT; instead, it
just redirects traffic to backends and preserves the VIP as the destination IP.

So, an agent exists on the node. It programs the node (either via iptables or routing tables) to
accept traffic destined for the VIP. However, this has a problem: all hairpin traffic
to the balanced servce is *always* handled by that backend, even if it is down
or otherwise out of rotation.

We want to withdraw the internal API service from google-routes redirection when
it's down, or else the node (i.e. kubelet) loses access to the apiserver VIP
and becomes unmanagable.


See `machine-config-operator/cmd/apiserver-watcher/gcp_handler.go`

### Azure

Azure L3LB does do DNAT, which presents a different problem: we can never reply
to hairpinned traffic. The problem looks something like this:

```
TCP SYN master-1 -> vip outgoing
(load balance happens)
TCP SYN master1 -> master1 incoming

(server socket accepts, reply generated)
TCP SYN, ACK master1 -> master1
```

This last packet is dropped, because the client socket is expecting a SYN,ACK with
a source IP of the VIP, not master1.

So, when the apiserver is up, we want to direct all local traffic to ourselves.
When it is down, we would like it to go over the load balancer.

See `machine-config-operator/cmd/apiserver-watcher/azure_handler.go`

## Functionality

The apiserver-watcher is installed on all the masters and monitors the
apiserver process /readyz, installing or removing the corresponding the
iptables rules depending on the endpoint status.
