# Core Beliefs - machine-config-operator

## Operating Principles

### 1. Operating System as Managed Infrastructure
The operating system itself is "just another Kubernetes component" that you can inspect and manage with `oc`.

**Implications**:
- All OS configuration goes through MachineConfig CRDs
- No SSH access required for configuration changes
- Configuration is declarative and version-controlled
- Cluster state drives OS state, not vice versa

**Example**: Changing SSH keys, adding systemd units, or modifying kubelet config all use MachineConfig objects rather than manual node access.

### 2. Immutability with Atomic Updates
OS updates use image-based rpm-ostree with atomic rollback capability.

**Implications**:
- Updates are transactional (all-or-nothing)
- Failed updates can be rolled back automatically
- Configuration changes trigger controlled node reboots
- No partial or corrupted system states

**Example**: pkg/daemon/rpm-ostree.go implements atomic OS updates via rpm-ostree.

### 3. Configuration Rendering is a Snapshot
All remote content is fetched and embedded into rendered MachineConfigs at render time.

**Implications**:
- No dynamic configuration at runtime
- All nodes in a pool see identical configuration
- Configuration drift is prevented
- Rendered configs are reproducible

**Example**: pkg/controller/template/render.go fetches remote Ignition files and embeds them.

## Non-Negotiable Constraints

### Security
- ✅ All configurations validated before application
- ✅ Secrets managed via Kubernetes Secret objects
- ✅ No remote execution without cluster approval
- ❌ Never allow manual node modification to persist across reboots

### Reliability
- ✅ Graceful degradation if updates fail
- ✅ Automatic rollback on failure
- ✅ Health checks before marking node ready
- ✅ Controlled rollout via MachineConfigPool maxUnavailable

### Correctness
- ✅ Configuration changes are idempotent
- ✅ Rendered configs are deterministic
- ✅ Pool updates respect PodDisruptionBudgets

## Patterns We Use

### Verify Before Implementing Pattern
**What**: Always verify actual data structures, file paths, and output formats before making assumptions

**When to use**: Before writing any code that processes or generates data from the system

**How to verify**:
1. Check reference documentation (e.g., output format specs)
2. Use grep to search for actual usage patterns in codebase
3. Look at similar implementations
4. Test assumptions with actual data/files

**Example in this repo**: Before implementing new controller logic, check existing controllers in pkg/controller/ for patterns.

**Why important**: Prevents implementing based on incorrect assumptions, which wastes time fixing later.

### Rendered Config Pattern
**What**: Multiple input MachineConfigs are merged into a single rendered config per pool

**When to use**: When multiple sources need to contribute to node configuration

**Example in this repo**: pkg/controller/template/render.go

**Why**: Ensures all nodes in a pool have identical configuration, prevents drift.

See: [MachineConfig Concept](../domain/concepts/machineconfig.md)

### Operator-Managed Lifecycle Pattern
**What**: One operator pod manages three sub-components (controller, server, daemon)

**When to use**: When multiple components need coordinated lifecycle management

**Example in this repo**: pkg/operator/sync.go

**Why**: Centralized upgrade coordination and version management.

See: [Operator Component](./components/machine-config-operator.md)

## Deprecated Patterns

### ❌ Direct Node SSH Configuration
**Don't**: SSH to nodes and manually edit configuration files
**Do**: Create MachineConfig objects that declare the desired state
**Why**: Manual changes are lost on reboot; declarative configs are enforced

### ❌ Dynamic Remote Configuration Sources
**Don't**: Reference remote Ignition configs that can change
**Do**: Embed remote content at render time (static snapshot)
**Why**: Prevents configuration drift between nodes and across time

## When to Break These Rules

1. Document in [agentic/decisions/](../decisions/)
2. Get consensus from team
3. Add to tech debt tracker if temporary
