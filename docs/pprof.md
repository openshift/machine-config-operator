# pprof Support in Machine Config Operator

The Machine Config Operator (MCO) components support runtime pprof profiling for debugging memory and performance issues in production environments.

## Overview

pprof profiling can be enabled dynamically for all MCO components by creating a ConfigMap in the `openshift-machine-config-operator` namespace.

## Supported Components

- machine-config-controller (MCC)
- machine-config-daemon (MCD)
- machine-config-operator (MCO)
- machine-config-server (MCS)
- machine-os-builder (MOB)

## Enabling pprof

### Basic Enablement (Default Ports)

Create the `enable-pprof` ConfigMap with no data to use default ports:

```bash
oc create configmap enable-pprof -n openshift-machine-config-operator
```

This will enable pprof on the following default ports:

- machine-config-controller: 6060
- machine-config-daemon: 6061
- machine-config-operator: 6062
- machine-config-server: 6063
- machine-os-builder: 6064

### Custom Port Configuration

To customize ports, create the ConfigMap with port configuration:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: enable-pprof
  namespace: openshift-machine-config-operator
data:
  # Global port override (applies to all components)
  pprof-port: "7070"

  # Per-component overrides (take precedence over global setting)
  # Can be used together with pprof-port - specify only the components you want to override
  machine-config-controller-port: "6060"
  machine-config-daemon-port: "6061"
```

**Port Configuration Priority:**

- Per-component settings (e.g., `machine-config-controller-port`) take precedence over the global `pprof-port` setting
- You can combine global and per-component settings: set `pprof-port` as a baseline and override specific components as needed
- If neither is specified for a component, the default port is used

### Behavior by Component

**machine-config-controller, machine-config-daemon, machine-config-server, machine-os-builder:**

- Deployments/DaemonSets are automatically rerendered with pprof flags when the ConfigMap is created
- Requires pod restart (automatic when deployment/daemonset is updated)

**machine-config-operator:**

- **Option 1 (Recommended):** ConfigMap-based enablement - pprof starts/stops dynamically as part of the reconciliation loop. **No pod restart required** - pprof will start automatically within seconds of creating the ConfigMap.
- **Option 2 (CVO disabled):** CLI flags - When the cluster-version-operator is disabled for debugging, you can start the machine-config-operator with `--enable-pprof` and `--pprof-port` flags. Requires restarting the operator pod with the new flags.
- **Note:** If both CLI flags and ConfigMap are present, CLI flags take precedence and ConfigMap-based reconciliation is disabled to avoid port conflicts.

## Accessing pprof Endpoints

All pprof servers bind to `127.0.0.1` (localhost only) for security. Access requires `oc port-forward`.

### Accessing machine-config-controller

```bash
# Port-forward to MCC deployment
oc port-forward -n openshift-machine-config-operator deployment/machine-config-controller 6060:6060

# In another terminal, access pprof
curl http://localhost:6060/debug/pprof/

# Or use go tool pprof
go tool pprof http://localhost:6060/debug/pprof/heap
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
```

### Accessing machine-config-daemon (on specific node)

```bash
# Find MCD pod on the node you want to profile
oc get pods -n openshift-machine-config-operator -l k8s-app=machine-config-daemon -o wide

# Port-forward to that specific pod
oc port-forward -n openshift-machine-config-operator machine-config-daemon-xxxxx 6061:6061

# Access pprof
curl http://localhost:6061/debug/pprof/
```

### Accessing machine-config-operator

**When using ConfigMap-based enablement (normal clusters):**

```bash
# Port-forward to MCO deployment
oc port-forward -n openshift-machine-config-operator deployment/machine-config-operator 6062:6062

# Access pprof
curl http://localhost:6062/debug/pprof/
```

**When using CLI flags (CVO disabled for debugging):**

```bash
# First, edit the MCO deployment to add pprof flags
oc edit deployment/machine-config-operator -n openshift-machine-config-operator

# Add to the args section:
# - "--enable-pprof"
# - "--pprof-port=6062"

# Wait for pod to restart
oc rollout status deployment/machine-config-operator -n openshift-machine-config-operator

# Then port-forward as normal
oc port-forward -n openshift-machine-config-operator deployment/machine-config-operator 6062:6062
curl http://localhost:6062/debug/pprof/
```

## Available pprof Endpoints

All components expose the standard pprof endpoints:

- `/debug/pprof/` - Index of available profiles
- `/debug/pprof/heap` - Heap memory allocation
- `/debug/pprof/goroutine` - Stack traces of all goroutines
- `/debug/pprof/profile` - CPU profile (30 seconds by default)
- `/debug/pprof/trace` - Execution trace
- `/debug/pprof/block` - Contention profiling
- `/debug/pprof/mutex` - Mutex profiling

## Common Profiling Tasks

### Investigating Memory Leaks

```bash
# Get heap profile
go tool pprof http://localhost:6060/debug/pprof/heap

# In pprof interactive mode:
(pprof) top10        # Show top 10 memory consumers
(pprof) list <func>  # Show source code for a function
(pprof) web          # Generate SVG visualization (requires graphviz)
```

### CPU Profiling

```bash
# 30-second CPU profile
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30

# In pprof interactive mode:
(pprof) top10
(pprof) web
```

### Goroutine Analysis

```bash
# Get goroutine dump
curl http://localhost:6060/debug/pprof/goroutine?debug=2 > goroutines.txt

# Or use pprof
go tool pprof http://localhost:6060/debug/pprof/goroutine
```

## Disabling pprof

To disable pprof, delete the ConfigMap:

```bash
oc delete configmap enable-pprof -n openshift-machine-config-operator
```

- For machine-config-controller, machine-config-daemon, machine-config-server, machine-os-builder: Deployments/DaemonSets will be rerendered without pprof flags (automatic pod restart)
- For machine-config-operator: pprof stops automatically in the next reconciliation loop (within seconds, no restart needed)

## Example Workflows

### Basic Usage (Default Ports)

```bash
# 1. Enable pprof with default ports
oc create configmap enable-pprof -n openshift-machine-config-operator

# 2. Wait for MCC pod to restart (or a few seconds for MCO)
oc rollout status deployment/machine-config-controller -n openshift-machine-config-operator

# 3. Port-forward to MCC
oc port-forward -n openshift-machine-config-operator deployment/machine-config-controller 6060:6060 &

# 4. Take a heap profile
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/heap

# 5. When done, disable pprof
oc delete configmap enable-pprof -n openshift-machine-config-operator
```

### Combining Global and Per-Component Port Settings

```bash
# Use port 7000 for all components, except MCC on 6060 and MCD on 6061
cat <<EOF | oc apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: enable-pprof
  namespace: openshift-machine-config-operator
data:
  # Set global baseline port
  pprof-port: "7000"
  # Override specific components
  machine-config-controller-port: "6060"
  machine-config-daemon-port: "6061"
EOF

# Result:
# - machine-config-controller: 6060 (per-component override)
# - machine-config-daemon: 6061 (per-component override)
# - machine-config-operator: 7000 (global setting)
# - machine-config-server: 7000 (global setting)
# - machine-os-builder: 7000 (global setting)
```

## References

- [Go pprof documentation](https://pkg.go.dev/net/http/pprof)
- [Profiling Go Programs](https://go.dev/blog/pprof)
- [Diagnostics - Profiling](https://go.dev/doc/diagnostics#profiling)
