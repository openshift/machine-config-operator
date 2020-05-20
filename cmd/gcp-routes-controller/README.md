# gcp-routes-controller

## Background

Google cloud load balancer is a L3LB that is special. It doesn't do DNAT; instead, it
just redirects traffic to backends and preserves the VIP as the destination IP.

So, an agent exists on the node. It programs the node (either via iptables or routing tables) to
accept traffic destined for the VIP. However, this has a problem: all hairpin traffic
to the balanced servce is *always* handled by that backend, even if it is down
or otherwise out of rotation.

We want to withdraw the internal API service from google-routes redirection when
it's down, or else the node (i.e. kubelet) loses access to the apiserver VIP
and becomes unmanagable.

## Functionality

The gcp-routes-controller is installed on all the masters and monitors the
apiserver process /readyz.

When /readyz fails, stops the VIP routing by writing `/run/gcp-routes/VIP.down`,
which tells openshift-gcp-routes to skip that vip
