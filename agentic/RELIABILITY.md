# Reliability - machine-config-operator

## Service Level Objectives (SLOs)

### Availability

**Target**: 99.9% uptime for MCO components
**Measurement**: ClusterOperator status (Available=True)
**Error Budget**: ~43 minutes/month downtime allowed

**What counts as down**:
- ClusterOperator Available=False
- MachineConfigController not reconciling
- MachineConfigDaemon not responding on nodes

### Update Success Rate

**Target**: >99% of node updates succeed without manual intervention
**Measurement**: MachineConfigPool status (UpdatedMachineCount / MachineCount)

**What counts as failure**:
- Node marked Degraded during update
- Update stuck for >2 hours
- Requires manual recovery (forcefile, node replacement)

### Update Latency

**Target**: p95 < 30 minutes per node (for config-only changes)
**Target**: p95 < 45 minutes per node (for OS updates with reboot)
**Measurement**: Time from desiredConfig annotation to currentConfig=desiredConfig

## Observability

### Metrics

**Key Metrics** (Prometheus):

- `mco_node_update_duration_seconds` - Time to update a node (histogram)
  - Type: Histogram
  - Labels: `pool`, `result` (success/failure)
  - Use: Track update performance, detect slow updates

- `mco_pool_update_progress` - Nodes updated vs. total in pool (gauge)
  - Type: Gauge
  - Labels: `pool`
  - Use: Monitor rollout progress

- `mco_config_drift_detected_total` - Config drift detections (counter)
  - Type: Counter
  - Labels: `node`, `file`
  - Use: Alert on unauthorized manual changes

- `mco_render_duration_seconds` - Time to render MachineConfig (histogram)
  - Type: Histogram
  - Labels: `pool`
  - Use: Detect rendering performance issues

**Dashboards**:
- OpenShift Console: Operators → machine-config → Metrics
- Grafana: MCO performance dashboard (if configured)

### Logging

**Log Levels**:
- **Error**: Failures requiring attention (update failed, config drift)
- **Warning**: Non-critical issues (drain timeout, temp failures)
- **Info**: Normal operations (update started, node drained)
- **Debug**: Detailed troubleshooting (config comparisons, rpm-ostree output)

**Structured Logging Fields**:
- `component`: `mco`, `mcc`, `mcd`, `mcs`
- `node`: Node name (for MCD logs)
- `pool`: MachineConfigPool name
- `config`: MachineConfig name

**Where to find logs**:
```bash
# MCO logs
oc logs -n openshift-machine-config-operator deployment/machine-config-operator

# MCC logs
oc logs -n openshift-machine-config-operator deployment/machine-config-controller

# MCD logs (specific node)
oc logs -n openshift-machine-config-operator daemonset/machine-config-daemon --selector=kubernetes.io/hostname=<node-name>

# MCS logs
oc logs -n openshift-machine-config-operator daemonset/machine-config-server
```

### Tracing

Not currently implemented. Considering OpenTelemetry for future releases.

## Alerts

### Critical Alerts

**Alert**: MCODegraded

- **Condition**: ClusterOperator Available=False for >15 minutes
- **Impact**: MCO cannot manage OS configuration or updates
- **Response**: Check MCO/MCC logs, verify API connectivity
- **Runbook**: [Operator Degraded](#operator-degraded-runbook)

**Alert**: MachineConfigPoolDegraded

- **Condition**: MachineConfigPool DegradedMachineCount > 0 for >30 minutes
- **Impact**: Nodes cannot update, may have config drift
- **Response**: Check node status, MCD logs, identify degraded nodes
- **Runbook**: [Pool Degraded](#pool-degraded-runbook)

**Alert**: NodeUpdateStuck

- **Condition**: Node stuck in "Working" state for >2 hours
- **Impact**: Update rollout stalled, other nodes blocked
- **Response**: Check MCD logs on stuck node, verify drain succeeded
- **Runbook**: [Update Stuck](#update-stuck-runbook)

### Warning Alerts

**Alert**: ConfigDriftDetected

- **Condition**: mco_config_drift_detected_total increases
- **Impact**: Potential unauthorized changes to nodes
- **Response**: Investigate which file changed, determine if intentional
- **Runbook**: [Config Drift](#config-drift-recovery)

**Alert**: UpdateSlowProgress

- **Condition**: MachineConfigPool updating for >4 hours with <50% progress
- **Impact**: Updates taking longer than expected
- **Response**: Check for slow drains, stuck evictions, resource constraints

## Runbooks

### Operator Degraded Runbook

**Symptoms**: ClusterOperator Available=False

**Diagnosis**:
1. Check operator status: `oc describe clusteroperator machine-config`
2. Check operator logs: `oc logs -n openshift-machine-config-operator deployment/machine-config-operator`
3. Verify MCO pod is running: `oc get pods -n openshift-machine-config-operator`

**Resolution**:
- If MCO pod crashlooping: Check logs for errors, verify RBAC
- If API connectivity issues: Verify network, check API server health
- If deployment missing: Verify CVO is running, check CVO logs

### Pool Degraded Runbook

**Symptoms**: MachineConfigPool shows DegradedMachineCount > 0

**Diagnosis**:
1. Identify degraded nodes: `oc get nodes -l machineconfiguration.openshift.io/state=Degraded`
2. Check node annotations: `oc get node/<node-name> -o yaml | grep machineconfiguration.openshift.io`
3. Check MCD logs on degraded node: `oc logs -n openshift-machine-config-operator daemonset/machine-config-daemon --selector=kubernetes.io/hostname=<node-name>`

**Resolution**:
- If config drift: Revert manual changes or update MachineConfig to match
- If update failed: Check MCD logs for specific error, may need forcefile
- If OS upgrade failed: Check rpm-ostree status, verify image accessibility

### Update Stuck Runbook

**Symptoms**: Node annotation shows state=Working for >2 hours

**Diagnosis**:
1. Check MCD logs for stuck node
2. Verify drain succeeded: `oc get node/<node-name>` (should show SchedulingDisabled)
3. Check for stuck pods: `oc get pods --all-namespaces --field-selector spec.nodeName=<node-name>`
4. Check PodDisruptionBudgets: `oc get pdb --all-namespaces`

**Resolution**:
- If drain stuck: Manually delete stuck pods (with caution)
- If PDB blocking: Temporarily reduce PDB or skip problematic workload
- If MCD hung: Restart MCD pod (delete pod, will recreate)
- Last resort: Create forcefile on node to bypass drain

### Config Drift Recovery

**Symptoms**: mco_config_drift_detected_total increased, node state=Degraded

**Diagnosis**:
1. Check which file changed: MCD logs will show file path
2. Compare on-disk content to MachineConfig spec
3. Determine if change was intentional

**Resolution**:
- If unintentional: Revert file to match MachineConfig, MCD will reconcile
- If intentional: Update/create MachineConfig to capture desired state
- Emergency: Create forcefile (`/run/machine-config-daemon-force`) to force reapply (causes reboot)

## Incident Response

1. **Detection**: Alert fires, user reports issue, or ClusterOperator status change
2. **Triage**: Check ClusterOperator status, identify affected pool/nodes
3. **Mitigation**: Follow runbook for specific issue, prioritize cluster stability
4. **Resolution**: Apply fix, verify nodes healthy, update complete
5. **Post-mortem**: Document incident, update runbooks, file bugs if needed

## Capacity Planning

**Current Capacity**: MCO scales with cluster size (MCC can have multiple replicas, MCD is DaemonSet)

**Growth Considerations**:
- Rendering time increases with number of MachineConfigs in pool
- Update rollout time scales linearly with number of nodes (controlled by maxUnavailable)

**Bottlenecks**:
- MCC rendering (CPU-bound): Increase MCC replicas if slow
- Network bandwidth for OS image pulls: Ensure sufficient egress to registry
- Drain time (PodDisruptionBudgets): May need to adjust PDB settings

## Disaster Recovery

**Backup**: MachineConfigs are stored in etcd (backed up by standard etcd backup)

**Recovery Time Objective (RTO)**: <1 hour (restore from etcd backup)

**Recovery Point Objective (RPO)**: Last successful etcd backup (typically every 1 hour)

**Recovery Procedure**:
1. Restore cluster from etcd backup
2. Verify MCO components running
3. Check MachineConfigPools status
4. Trigger update if needed to reach desired state

## ClusterOperator Status Reporting

MCO reports status via ClusterOperator resource following OpenShift conventions:

**Conditions**:
- **Available**: MCO is functional and can manage nodes
- **Progressing**: Update rollout in progress
- **Degraded**: One or more components or nodes in bad state
- **Upgradeable**: Safe to upgrade to newer version

**How to check**:
```bash
oc get clusteroperator machine-config
oc describe clusteroperator machine-config
```

## Related Documentation

- [Architecture](../ARCHITECTURE.md) - System structure
- [Domain Concepts](./domain/) - Understanding MachineConfig, pools, etc.
- [Debugging Guide](./DEVELOPMENT.md#debugging) - Troubleshooting procedures
