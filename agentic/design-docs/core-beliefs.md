# Core Beliefs - machine-config-operator

## Operating Principles

### 1. Operating System as Kubernetes Component

The OS is not a separate concern—it's managed declaratively via Kubernetes APIs just like applications.

**Implications**:
- Configuration changes go through git → MachineConfig → automated rollout
- No SSH-ing to nodes for config changes
- OS updates are cluster-managed, not host-managed

**Example**: Changing sysctl settings via `kubectl apply -f machineconfig.yaml` rather than manually editing `/etc/sysctl.conf`

### 2. Image-Based OS Updates

Use immutable, atomic OS images via rpm-ostree rather than package-by-package updates.

**Implications**:
- Entire OS is versioned as a container image
- Atomic updates with automatic rollback on failure
- OS state is reproducible across all nodes in a pool

**Example**: OS updates pull container image `quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:...` containing full OS tree

### 3. Controlled, Gradual Rollout

Never update all nodes simultaneously—respect `maxUnavailable` to prevent cluster-wide outages.

**Implications**:
- Only N nodes update at once (default: 1)
- UpdateController coordinates across pool
- Workloads remain available during updates

**Example**: In 100-node worker pool with `maxUnavailable: 5`, only 5 workers update simultaneously

### 4. Verify Before Implementing Pattern

**What**: Always verify actual data structures, file paths, annotation names, and output formats before making assumptions

**When to use**: Before writing any code that processes or generates data from the system

**How to verify**:
1. Check reference documentation for canonical formats (Ignition spec, MachineConfig CRD)
2. Use grep to search for actual usage patterns: `grep -r "machineconfiguration.openshift.io/state" pkg/`
3. Look at similar implementations in existing controllers
4. Test assumptions with actual API objects or files

**Example in this repo**: Before assuming node annotation format, grep for existing usage:
```bash
$ grep -r "machineconfiguration.openshift.io/currentConfig" pkg/
pkg/daemon/constants.go:	CurrentMachineConfigAnnotationKey = "machineconfiguration.openshift.io/currentConfig"
pkg/controller/node/status.go:	currentConfig := node.Annotations["machineconfiguration.openshift.io/currentConfig"]
```

**Why important**: Prevents implementing based on incorrect assumptions about annotation names, API fields, file paths, which wastes time fixing later

## Non-Negotiable Constraints

### Security

- ✅ All node access via Kubernetes RBAC (no SSH keys except for emergency access core user)
- ✅ Ignition configs served only to authenticated nodes (client cert required)
- ✅ Config drift detected and marked Degraded (prevents unauthorized manual changes)
- ❌ Never bypass MachineConfig validation (could brick nodes)
- ❌ Never skip drain before reboot (protects workloads)

### Reliability

- ✅ Drain nodes before reboot (respect PodDisruptionBudgets)
- ✅ Validate config before applying (preflight checks)
- ✅ Automatic rollback on rpm-ostree failure
- ✅ Config drift detection via fsnotify
- ✅ ClusterOperator status must reflect true state

### Correctness

- ✅ Rendered configs must be deterministic (same inputs → same output)
- ✅ On-disk state must match MachineConfig (drift is error state)
- ✅ OS version must match OSImageURL after update
- ✅ All file writes are atomic (avoid partial state)

## Patterns We Use

### Ignition for Declarative Configuration

**What**: Use Ignition (not bash scripts) for OS configuration

**When**: Any file writes, systemd unit creation, user management on nodes

**Why**: Declarative, idempotent, validated at parse time

**Example**: Creating `/etc/my-config.conf`:
```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-custom-config
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
      - path: /etc/my-config.conf
        mode: 0644
        contents:
          source: data:,key%3Dvalue
```

See: [Ignition concept doc](../domain/concepts/ignition.md)

### Annotation-Based Coordination

**What**: Use node annotations to coordinate between MCC and MCD (not ConfigMaps or Secrets)

**When**: Tracking update state across controller and daemon

**Why**: Atomic updates, survives node reboot, visible in `oc describe node`

**Example**: MCD sets currentConfig after successfully applying update:
```go
node.Annotations["machineconfiguration.openshift.io/currentConfig"] = "rendered-worker-abc123"
node.Annotations["machineconfiguration.openshift.io/state"] = "Done"
```

See: `pkg/daemon/constants.go` for all annotation keys

### Config Drift as Degraded State

**What**: Detect filesystem changes via fsnotify, mark node Degraded if drift detected

**When**: MCD is running and monitoring active MachineConfig

**Why**: Manual changes break declarative model, must be visible/blocked

**Example**: If admin manually edits `/etc/kubernetes/kubelet.conf`, MCD detects within seconds and sets `state=Degraded`

See: `pkg/daemon/drift.go`, [Config Drift Detection doc](../../docs/MachineConfigDaemon.md#config-drift-detection)

### Template + Render + User Pattern

**What**: Three sources of MachineConfigs:
1. **Template**: OpenShift-generated (e.g., `00-master`, `00-worker`)
2. **Render**: Controller-generated (e.g., `99-master-kubelet`)
3. **User**: Admin-created (e.g., `99-custom-sysctl`)

**When**: Organizing MachineConfigs

**Why**: Clear ownership, prevents conflicts, predictable merge order

**Example**:
- Template: `00-master` (from internal templates)
- Controller-generated: `99-master-generated-kubelet` (from KubeletConfig CRD)
- User: `99-master-custom-sysctl` (user-created)

RenderController merges all three → `rendered-master-{hash}`

See: [RenderController doc](../../docs/MachineConfigController.md#rendercontroller)

## Deprecated Patterns

### ❌ SSH-based Configuration

**Don't**: SSH to nodes and manually edit config files

**Do**: Create MachineConfig and let MCO apply it

**Why**: Breaks declarative model, causes config drift, not auditable

### ❌ Bypassing Drain

**Don't**: Use `--force` or skip drain before reboot

**Do**: Let MCD perform proper drain (respects PDBs)

**Why**: Protects workload availability, prevents etcd quorum loss

### ❌ Manually Running rpm-ostree

**Don't**: SSH to node and run `rpm-ostree upgrade` directly

**Do**: Update OSImageURL in MachineConfig and let MCD apply it

**Why**: MCO coordinates updates across cluster, validates results, handles rollback

## When to Break These Rules

**Rare exceptions allowed**:

1. **Emergency node recovery**: If MCD is crashlooping and node is unrecoverable, may need manual intervention
   - Document in [tech debt tracker](../exec-plans/tech-debt-tracker.md)
   - Create ADR explaining why

2. **Development/testing**: Local development may require manual changes
   - Never in production
   - Document workaround in dev docs

3. **Upstream bugs**: If rpm-ostree/Ignition has critical bug
   - File upstream issue
   - Create ADR with mitigation plan
   - Remove workaround when fixed

**Process for exceptions**:
1. Document in ADR: `agentic/decisions/adr-NNNN-exception-{reason}.md`
2. Add to tech debt tracker if temporary
3. Get team consensus
4. Monitor for when exception can be removed
