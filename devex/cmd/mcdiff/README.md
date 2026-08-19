# mcdiff

MCDiff explains what the Machine Config Operator (MCO) thinks a file should contain, which MachineConfig last wrote it, and how that differs from a local copy, a live node, or a must-gather archive.

The MCD reports on-disk mismatches as byte counts on purpose (those files can hold secrets). That is enough to know something drifted, and not enough to debug it. MCDiff is the explanation layer: it reads the **rendered** MachineConfig as source of truth, attributes last-writer using MCO merge order, and prints a unified diff when you ask it to compare.

This is a `devex` helper. Do not use it as an unsupervised production remediation tool.

## Build

From the MCO repo root:

```console
go build -o mcdiff ./devex/cmd/mcdiff
```

Or: `make install-helpers`

## Commands

```console
mcdiff file PATH --pool POOL [flags]
mcdiff node NODE [flags]
mcdiff diff MC1 MC2
mcdiff completion bash|zsh|fish|powershell
```

`file` inspects one path. `node` scans every Ignition file in the node's rendered MachineConfig against the host filesystem. `diff` is the older helper that runs `dyff` between two MachineConfig objects.

## Examples

Inspect expected content and last writer (does not print file bytes by default):

```console
mcdiff file /etc/ssh/sshd_config --pool worker
```

Compare against a live node (execs into the machine-config-daemon pod, host root at `/rootfs`):

```console
mcdiff file /etc/ssh/sshd_config --pool worker --node worker-0
```

Compare against a local file:

```console
mcdiff file /etc/ssh/sshd_config --pool worker --from-file ./sshd_config
```

Offline analysis from an unpacked must-gather:

```console
mcdiff file /etc/ssh/sshd_config --pool worker --must-gather ./must-gather.local
mcdiff file /etc/ssh/sshd_config --pool worker --node worker-0 --must-gather ./must-gather.local
```

Print expected bytes or JSON:

```console
mcdiff file /etc/ssh/sshd_config --pool worker --show-content
mcdiff file /etc/ssh/sshd_config --pool worker -o json
```

Scan every managed file on a node (pool is detected from node labels):

```console
mcdiff node worker-0
mcdiff node worker-0 --pool worker
mcdiff node worker-0 --show-diffs
mcdiff node worker-0 --must-gather ./must-gather.local --pool worker
mcdiff node worker-0 -o json
```

Replace the KCS `oc debug` + `jq` + `base64`/`urldecode` walkthrough for a degraded MachineConfigDaemon:

```console
mcdiff file /etc/chrony.conf --pool worker --node worker-0
mcdiff file /etc/resolv.conf --pool worker --node worker-0
mcdiff file /etc/kubernetes/kubelet-ca.crt --pool master --node master-0
```

Ignition `data:,…` percent-encoding and `data:text/plain;charset=utf-8;base64,…` are decoded automatically. Missing host files (`could not stat file`) are reported as **MISSING ON NODE** without failing the command. Mode drift (for example 0644 vs 0755) is reported next to size and content deltas.

## Flag matrix

`--pool` is required for `file`. For `node` it is optional: omitted means detect the pool from the node's labels the same way the Machine Config Operator does. `--show-content` (file) / `--show-diffs` (node) and `-o json` are optional in every valid mode.

| Mode | `--from-file` | `--node` | `--must-gather` | Result |
| --- | --- | --- | --- | --- |
| Live inspect | | | | Expected bytes + last writer from the cluster |
| Local compare | yes | | | Diff expected vs a local file |
| Live node compare | | yes | | Diff expected vs the file on the node |
| Offline inspect | | | yes | Same as live inspect, from must-gather CRs (no kubeconfig) |
| Offline node compare | | yes | yes | Diff expected vs a must-gather node snapshot |
| **Invalid** | yes | yes | | Error: `cannot use --from-file and --node together` |
| **Invalid** | yes | | yes | Error: `cannot use --must-gather and --from-file together` |
| **Invalid** | yes | yes | yes | Same `--from-file` / `--node` error |

`mcdiff node NODE` always compares against the node (live or must-gather). There is no `--from-file` on this command.

| Mode | `--pool` | `--must-gather` | `--show-diffs` | Result |
| --- | --- | --- | --- | --- |
| Live whole-node scan | optional | | | Summary of every managed file vs the node |
| Live whole-node scan with diffs | optional | | yes | Same, plus unified diffs for mismatches |
| Offline whole-node scan | recommended | yes | | Same, from must-gather CRs and snapshots |
| Pool override | yes | | | Skip label-based pool detection |

Pass `--pool` when the node is unassigned, Windows, or matches more than one custom pool.

Live modes use the standard kubeconfig flags (`--kubeconfig`, `--context`, `KUBECONFIG`). `--must-gather` skips kubeconfig.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Inspection succeeded. Includes MATCH, CONTENT MISMATCH, MODE MISMATCH, unmanaged paths, MISSING ON NODE, CLEAN, and DRIFT DETECTED. |
| non-zero | The tool could not perform the inspection: invalid flags, missing pool / rendered MachineConfig / node, unreadable `--from-file` or must-gather directory, RBAC, or network errors. |

MCDiff is a diagnostic tool, not a CI gate. A drift finding is still a successful inspection.

## Must-gather caveat

Standard `oc adm must-gather` archives include:

- Cluster-scoped MachineConfig and MachineConfigPool YAML under `cluster-scoped-resources/machineconfiguration.openshift.io/`
- Node objects under `cluster-scoped-resources/core/nodes/`
- On degraded nodes, MCO’s `machine_config_ondisk/<node>/currentconfig`

They do **not** snapshot the entire host `/etc` tree. `--node` with `--must-gather` (and `mcdiff node --must-gather`) only diffs a host file when the archive contains a snapshot (`nodes/<node>/host/...`, `host_files/<node>/...`, `machine_config_ondisk/<node>/files/...`) or when that path can be decoded from `currentconfig`. Files without a snapshot are reported as missing. Extract the archive to a directory first; do not pass a tarball.

## Shell completion

```console
source <(mcdiff completion bash)
source <(mcdiff completion zsh)
mcdiff completion fish | source
```

## Testing

Unit tests, live-cluster scenarios, and must-gather steps for reviewers and QE: [TESTING.md](./TESTING.md).

Suggested PR title and description: [PR_DESCRIPTION.md](./PR_DESCRIPTION.md).

Expected file contents are omitted unless `--show-content` is set. Unified diffs from `--from-file`, `--node`, or `mcdiff node --show-diffs` do print changed lines, because that is the comparison result. Treat those outputs as sensitive.
