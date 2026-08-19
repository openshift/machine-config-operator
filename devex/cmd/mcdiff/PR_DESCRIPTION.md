# PR Title

```
devex/mcdiff: Add MachineConfig diff, attribution, and node drift scanner CLI
```

# Short Summary

Replaces opaque MachineConfigDaemon byte-count errors with file-level expected content, last-writer attribution, and whole-node drift scanning.

The existing `mcdiff diff MC1 MC2` dyff helper is unchanged. This PR adds `mcdiff file` and `mcdiff node` as a `devex` diagnostic: it consumes the **rendered** MachineConfig as source of truth (no client-side re-merge), attributes last writer using MCO merge order, and diffs against a local file, a live node (MCD `/rootfs`), or an unpacked must-gather archive.

This is not remediation, MachineConfig editing, or a CI gate. Drift findings exit 0.

# Scope of Changes

All new and updated code lives under `devex/cmd/mcdiff`:

| Package | Role |
| --- | --- |
| `devex/cmd/mcdiff` | CLI: `file`, `node`, existing `diff`, shell completion |
| `internal/cluster` | Load pool rendered MC (`status.configuration`, else `spec.configuration`) |
| `internal/ignition` | Decode Ignition files (`data:` base64 and percent-encoded) |
| `internal/attribution` | Last-writer using MergeMachineConfigs order |
| `internal/diff` | Byte compare, unified diff, mode compare |
| `internal/node` | Live node read via machine-config-daemon exec (`/rootfs`) |
| `internal/mustgather` | Offline Getter + NodeReader from unpacked archives |
| `internal/scanner` | Whole-node scan + MCP detection from node labels |
| `internal/report` | Text and JSON reports |

Docs: `README.md`, `TESTING.md`, this file.

# Verification Checklist for Reviewers

- [ ] `go test ./devex/cmd/mcdiff/...` passes
- [ ] `mcdiff file` works with `--pool`, `--from-file`, `--node`, and `--must-gather`
- [ ] `mcdiff node` performs whole-node scan
- [ ] Base64 and percent-encoded Ignition payloads decode seamlessly
- [ ] Offline must-gather mode functions without kubeconfig

See [TESTING.md](./TESTING.md) for unit, live-cluster, and must-gather steps.

# Local verification (author)

From the MCO repo root:

```console
go build ./devex/cmd/mcdiff/...
go vet ./devex/cmd/mcdiff/...
gofmt -s -l devex/cmd/mcdiff/
go test ./devex/cmd/mcdiff/... -count=1
```

# Notes for reviewers

- Expected bytes always come from the rendered MachineConfig, never `MergeMachineConfigs`.
- Last-writer uses `configuration.source` and the same fragment sort as `pkg/controller/common.MergeMachineConfigs`.
- Live `--node` execs into the existing `machine-config-daemon` pod (`k8s-app=machine-config-daemon` in `openshift-machine-config-operator`), host tree at `/rootfs`.
- Standard must-gather does **not** dump `/etc`. Paths without a snapshot are **MISSING ON NODE**.
- Unified diffs and `--show-content` can include secrets. Treat output as sensitive.
- Do not run the live drift-injection scenario from TESTING.md on a production cluster.
