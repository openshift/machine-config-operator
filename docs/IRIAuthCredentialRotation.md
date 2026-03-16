# IRI Registry Authentication Credential Rotation

This document describes how the Internal Release Image (IRI) registry
authentication credentials are managed and rotated by the
`InternalReleaseImageController`.

## Background

The IRI registry runs on each control plane node behind the `api-int` VIP
(port 22625). Both master and worker nodes pull images from it using
credentials stored in the global pull secret (`openshift-config/pull-secret`).
Because `api-int` load-balances across **all** master nodes, updating
credentials naively causes intermittent authentication failures: a request
may reach a node that still has the old htpasswd while the pull secret already
contains the new password, or vice versa.

The distribution registry's htpasswd implementation stores entries in a
`map[string][]byte`, so duplicate usernames overwrite each other (last wins).
This means we cannot simply list the same username twice with different
passwords; instead, dual credentials must use **different usernames**.

## Desired-vs-Current Pattern

The controller follows the same desired-vs-current reconciliation pattern used
elsewhere in the MCO (e.g., rendered MachineConfig vs node annotations):

| Secret | Role |
|---|---|
| `openshift-machine-config-operator/internal-release-image-registry-auth` (auth secret) | Holds the **desired** password in `data.password` and the htpasswd file in `data.htpasswd`. |
| `openshift-config/pull-secret` (global pull secret) | Holds the **current** active credentials (as deployed on nodes via rendered MachineConfig). |

The controller reads the **deployed** state from the rendered MachineConfig's
ignition content (`/var/lib/kubelet/config.json`), not from the pull secret API
object, to accurately determine what is actually on nodes.

## Generation-Numbered Usernames

To support dual htpasswd entries during rotation, usernames carry a generation
counter:

- `openshift` — generation 0 (initial)
- `openshift1` — generation 1
- `openshift2` — generation 2
- ...

Each rotation increments the generation, producing a new username that coexists
with the previous one in the htpasswd file.

## Rotation Phases

When the auth secret's desired password differs from the deployed password, the
controller performs a three-phase rotation:

### Phase 1: Deploy Dual Htpasswd

The controller generates an htpasswd file containing **two entries**: one for
the currently deployed credentials (old username + old password) and one for
the new credentials (next-generation username + new password). It writes this
dual htpasswd to `data.htpasswd` in the auth secret.

The renderer reads the htpasswd from the auth secret, so the next MachineConfig
render includes both credentials. As the MachineConfigPool rolls out, each node
receives the dual htpasswd. During this rollout, both old and new passwords are
accepted regardless of which node handles the request.

### Phase 2: Update Pull Secret

Once **all** MachineConfigPools report that every machine is updated
(`UpdatedMachineCount == MachineCount == ReadyMachineCount`), the controller
updates the global pull secret with the new username and password. This is safe
because all nodes now accept both passwords.

The pull secret update triggers the template controller to re-render, creating
a new rendered MachineConfig. Another MCP rollout propagates the updated pull
secret to all nodes.

### Phase 3: Clean Up to Single Htpasswd

On subsequent syncs, the controller reads the deployed pull secret from the
rendered MachineConfig. Once the deployed password matches the desired password
(i.e., the Phase 2 rollout is complete), the controller cleans up the dual
htpasswd to a single entry containing only the new credentials. This removes
the old password, eliminating the security risk of leaving stale credentials.

## State Diagram

```
              ┌────────────────────────┐
              │ No IRI entry in pull   │
              │ secret (first time)    │
              └──────────┬─────────────┘
                         │ merge directly
                         v
              ┌────────────────────────┐
              │ Deployed == Desired    │◄──────────────────┐
              │ (steady state)         │                   │
              └──────────┬─────────────┘                   │
                         │ auth secret password changes    │
                         v                                 │
              ┌────────────────────────┐                   │
              │ Phase 1: Write dual    │                   │
              │ htpasswd to auth secret│                   │
              └──────────┬─────────────┘                   │
                         │ wait for MCP rollout            │
                         v                                 │
              ┌────────────────────────┐                   │
              │ Phase 2: Update pull   │                   │
              │ secret with new creds  │                   │
              └──────────┬─────────────┘                   │
                         │ wait for MCP rollout            │
                         v                                 │
              ┌────────────────────────┐                   │
              │ Phase 3: Clean up dual │                   │
              │ htpasswd → single entry│───────────────────┘
              └────────────────────────┘
```

## Triggering a Credential Rotation

To rotate IRI registry credentials, update the `password` field in the auth
secret:

```bash
oc -n openshift-machine-config-operator patch secret internal-release-image-registry-auth \
  --type merge -p '{"data":{"password":"'$(echo -n "new-password" | base64)'"}}'
```

The controller detects the mismatch between the desired password (auth secret)
and the deployed password (rendered MachineConfig) and automatically performs
the three-phase rotation. No manual intervention is required after updating
the secret.

**Important:** Only modify the `password` field. The `htpasswd` field is
controller-managed. If the htpasswd is manually edited, the controller will
detect the invalid hash via `bcrypt.CompareHashAndPassword` and regenerate it.

## User Edits During Rotation

The `password` and `htpasswd` fields in the auth secret can be changed at any
point during rotation. The controller handles all cases safely:

- **Password changes** are detected via `HtpasswdHasValidEntry`, which uses
  `bcrypt.CompareHashAndPassword` to verify the htpasswd hash matches the
  desired password. If it doesn't match (password changed since the dual
  htpasswd was written), the dual htpasswd is regenerated. Clients are never
  affected because the pull secret always contains the old deployed credentials,
  which are preserved as one of the two htpasswd entries in every regeneration.

- **Htpasswd edits** are detected by the same `HtpasswdHasValidEntry` check.
  Invalid or tampered hashes cause regeneration. The controller is the sole
  owner of the htpasswd field.

- **Cross-pool inconsistency** (e.g., password changed after the pull secret
  was updated but before all pools re-rendered) is detected by
  `getDeployedIRICredentials`, which returns `ErrInconsistentIRICredentials`.
  The controller waits for convergence before making any rotation decisions.

### Per-Phase Analysis

**Phase 1** (dual htpasswd written, MCP rolling out): Password or htpasswd
changes cause `HtpasswdHasValidEntry` to fail. Dual htpasswd is regenerated
with the latest desired password. New MCP rollout starts. Pull secret is
unchanged.

**Phase 2** (all MCPs updated, pull secret about to be updated): Password or
htpasswd changes cause dual htpasswd regeneration **before** the pull secret
update code is reached, so the pull secret is never updated with a stale
password. Goes back to Phase 1.

**Phase 2b** (pull secret updated, re-render in progress): Different pools may
have different rendered MCs. `getDeployedIRICredentials` returns
`ErrInconsistentIRICredentials`. Controller takes no action and waits for
convergence. Password or htpasswd edits during this window are ignored until
pools converge. After all pools converge, the next sync picks up the correct
deployed state and reconciles.

**Phase 3** (deployed matches desired, cleaning up dual htpasswd): Password
changes cause a new rotation (Phase 1) instead of cleanup, skipping cleanup and
starting fresh. Htpasswd edits are detected by the `HtpasswdHasValidEntry`
check and repaired to a single valid entry.

## Safety Guarantees

- **No authentication deadlock**: Dual htpasswd ensures both old and new
  passwords are accepted during the entire rollout window. The pull secret
  (what clients use) always contains the old credentials, which are always
  present in every version of the htpasswd.
- **No stale credentials**: The dual htpasswd is cleaned up to a single entry
  once the rotation is fully deployed, removing the old password.
- **Accurate deployed state**: The controller reads credentials from the
  rendered MachineConfig ignition content, not the API object, avoiding race
  conditions between API updates and node rollouts.
- **Cross-pool consistency**: `getDeployedIRICredentials` checks all pools'
  rendered MachineConfigs and returns `ErrInconsistentIRICredentials` if
  different pools have different IRI credentials (e.g., one pool's rendered
  MC was re-rendered with the updated pull secret while another still has
  the old one). When this occurs, the controller treats it as a rollout in
  progress and waits for convergence before making any rotation decisions.
- **All pools checked**: Both master and worker MachineConfigPools must
  complete rollout before advancing phases, because workers are also IRI
  registry clients via `api-int`.
- **Htpasswd self-healing**: If the htpasswd is manually edited, the
  controller detects the invalid hash via `bcrypt.CompareHashAndPassword`
  and regenerates it to match the expected credentials.
- **Mid-rotation resilience**: Changing the desired password during an active
  rotation regenerates the dual htpasswd without causing deadlock. The
  `HtpasswdHasValidEntry` check uses bcrypt hash comparison (not string
  equality) to detect when the desired password has changed since the dual
  htpasswd was written.
