# Testing MCDiff

Instructions for peer reviewers and QE. Run all commands from the machine-config-operator repository root unless noted.

MCDiff is a diagnostic. A content mismatch, mode mismatch, or **MISSING ON NODE** is a successful inspection (exit 0). Non-zero means the tool could not inspect (bad flags, missing pool/rendered MC/node, RBAC, or I/O).

Do **not** run the live drift-injection scenario on a production cluster. Use a disposable cluster or skip to unit tests and must-gather.

## 1. Local unit and package tests

```console
go test ./devex/cmd/mcdiff/... -v -count=1
```

Expect every package under `devex/cmd/mcdiff` to print `PASS`, including:

- `internal/ignition` — base64 and percent-encoded `data:` URLs
- `internal/diff` — content, CRLF, trailing newline, mode
- `internal/scanner` — all-match, mismatch, missing, mode, pool detection
- `devex/cmd/mcdiff` — `file` and `node` CLI, JSON, must-gather fixtures

Optional gates used before opening a PR:

```console
go build ./devex/cmd/mcdiff/...
go vet ./devex/cmd/mcdiff/...
gofmt -s -l devex/cmd/mcdiff/
```

`gofmt -s -l` should print nothing.

## 2. Binary compilation

Build the whole `main` package (not `main.go` alone; the command is split across several files):

```console
mkdir -p bin
go build -o bin/mcdiff ./devex/cmd/mcdiff
./bin/mcdiff --help
./bin/mcdiff file --help
./bin/mcdiff node --help
```

Or: `make install-helpers` (installs all `devex/cmd` helpers).

Confirm the help lists `file`, `node`, `diff`, and `completion`.

## 3. Live OpenShift cluster testing

Prerequisites:

- `oc` logged in with rights to get MachineConfigPools, MachineConfigs, Nodes, and to exec into `machine-config-daemon` pods in `openshift-machine-config-operator`
- Standard kubeconfig (`KUBECONFIG`, `--kubeconfig`, or `--context`)

### a. Identify a worker node

```console
oc get nodes -l node-role.kubernetes.io/worker
```

Pick one `Ready` node. In the steps below, replace `<node-name>` with that name (for example `worker-0`).

### b. Inspect expected content (no host read)

```console
./bin/mcdiff file /etc/ssh/sshd_config --pool worker
```

Expect: pool `worker`, a `rendered-worker-*` MachineConfig, `Exists: yes`, last writer (often `99-worker-ssh` or similar), expected content omitted unless `--show-content`.

### c. Introduce intentional drift (disposable cluster only)

```console
oc debug node/<node-name> -- chroot /host sh -c "echo '# drift' >> /etc/ssh/sshd_config"
```

The node may go degraded. That is the point of this scenario.

### d. Live single-file diff

```console
./bin/mcdiff file /etc/ssh/sshd_config --pool worker --node <node-name>
```

Expect: `CONTENT MISMATCH`, a size delta, and a unified diff that includes `+# drift`. Exit 0.

Also useful KCS-style paths (no extra drift required):

```console
./bin/mcdiff file /etc/chrony.conf --pool worker --node <node-name>
./bin/mcdiff file /etc/resolv.conf --pool worker --node <node-name>
```

### e. Whole-node scan

```console
./bin/mcdiff node <node-name> --show-diffs
```

Expect: `DRIFT DETECTED`, `/etc/ssh/sshd_config` in mismatched files, last writer, size delta, and a unified diff because `--show-diffs` is set. Other managed files should `MATCH` unless the node was already drifted.

If pool detection fails (`not assigned` / multiple custom pools), add `--pool worker`.

### f. Clean up drift

```console
oc debug node/<node-name> -- chroot /host sh -c "sed -i '/# drift/d' /etc/ssh/sshd_config"
```

Re-run the file diff and node scan. Expect `MATCH` / `CLEAN` unless other drift remains. The MCD may take a short time to clear degraded.

## 4. Offline must-gather testing

Standard `oc adm must-gather` does **not** snapshot all of `/etc`. Offline `--node` / `mcdiff node` only diffs a path when the archive has a host snapshot (`nodes/<node>/host/...`, `host_files/<node>/...`, `machine_config_ondisk/<node>/files/...`) or that path can be decoded from `currentconfig`. Other managed paths are **MISSING ON NODE**. Extract the tarball first; do not pass a `.tar.gz`.

### a. Unpack

```console
mkdir -p /tmp/must-gather.local
tar -C /tmp/must-gather.local -xf must-gather.tar.gz
```

Use the directory that contains `cluster-scoped-resources` (sometimes one level down under an image directory).

### b. Offline inspect (no kubeconfig)

```console
unset KUBECONFIG
./bin/mcdiff file /etc/ssh/sshd_config --pool worker --must-gather /tmp/must-gather.local
```

Expect: rendered MC and last writer from YAML in the archive. Does not need a live cluster.

### c. Offline whole-node scan

```console
./bin/mcdiff node worker-0 --must-gather /tmp/must-gather.local --pool worker
```

Replace `worker-0` with a node name present in the archive (`cluster-scoped-resources/core/nodes/` or `nodes/`). Pass `--pool` if the Node object has no role labels.

Expect: scan completes (exit 0). Files without snapshots are listed as missing. That is expected for a stock must-gather.

## QE pass / fail

| Check | Pass |
| --- | --- |
| Unit tests | All `devex/cmd/mcdiff` packages PASS |
| `file --pool` | Prints pool, rendered MC, last writer; omits file bytes |
| `file --from-file` | MATCH or CONTENT MISMATCH with unified diff |
| `file --node` after step c | CONTENT MISMATCH, `+# drift`, exit 0 |
| `file --node` missing path | **MISSING ON NODE**, exit 0 |
| `node` scan after step c | DRIFT DETECTED includes sshd_config |
| Encoding | Unit tests for base64 and `data:,` percent-encoding PASS; live inspect does not require manual decode |
| Must-gather | Inspect works with `KUBECONFIG` unset; no API server required |
| Invalid flags | `cannot use --from-file and --node together` |

Treat `--show-content` and unified diffs as sensitive. Do not paste them into public bugs.
