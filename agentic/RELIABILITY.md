# Reliability - machine-config-operator

## Service Level Objectives (SLOs)

### Availability
**Target**: 99.9% uptime (ClusterOperator status=Available)
**Measurement**: ClusterOperator status.conditions[type=Available]
**Error Budget**: ~43 minutes/month

### Update Success Rate
**Target**: >98% of node updates succeed without manual intervention
**Measurement**: Ratio of successful updates to total updates attempted

### Pool Update Latency
**Target**: p95 < 30 minutes for full pool update (10 nodes)
**Measurement**: Time from rendered config change to all nodes Updated=true
**Note**: Depends on maxUnavailable and node reboot time

## Observability

### Metrics

**Key Metrics** (Prometheus/OpenMetrics):

- `mco_machine_config_daemon_state` - Daemon state (Working, Done, Degraded)
  - Type: Gauge
  - Labels: node, state
  - Use: Monitor node update progress

- `mco_pool_update_count_total` - Total pool updates
  - Type: Counter
  - Labels: pool
  - Use: Track update frequency

- `mco_pool_update_duration_seconds` - Pool update duration
  - Type: Histogram
  - Labels: pool, result(success/failure)
  - Use: Monitor update performance

- `mco_rendered_config_generation` - Current rendered config generation
  - Type: Gauge
  - Labels: pool
  - Use: Detect config changes

**Dashboards**:
- OpenShift Console: Operators → Machine Config → Metrics tab
- Prometheus: Direct query UI
- Grafana: MCO dashboard (if deployed)

### Logging

**Log Levels**:
- **Error**: Update failures, API errors, unrecoverable states
- **Warning**: Retryable failures, degraded state, slow operations
- **Info**: Update progress, config changes, state transitions
- **Debug**: Detailed reconciliation steps, API calls

**Structured Logging Fields**:
- `component`: daemon | controller | server | operator
- `operation`: update | render | serve | sync
- `node`: node name (daemon logs)
- `pool`: pool name (controller logs)

**Log Locations**:
```bash
# Controller logs
oc logs -n openshift-machine-config-operator deploy/machine-config-controller

# Daemon logs (on node)
oc debug node/<node> -- chroot /host journalctl -u machine-config-daemon

# Server logs
oc logs -n openshift-machine-config-operator deploy/machine-config-server
```

### Tracing

Not currently implemented. Future consideration for distributed tracing of config rendering and application.

## Alerts

### Critical Alerts

**Alert**: MCODaemonDegraded
- **Condition**: Daemon in Degraded state >15 minutes
- **Impact**: Node updates failing, configuration not applied
- **Response**: Check daemon logs, node conditions, rendered config validity
- **Runbook**: docs/runbooks/mco-daemon-degraded.md (to be created)

**Alert**: MCOPoolUpdateStuck
- **Condition**: Pool in Updating state >2 hours
- **Impact**: Configuration changes not rolling out
- **Response**: Check node update progress, drain failures, maxUnavailable constraints
- **Runbook**: Check `oc describe machineconfigpool <pool>`

**Alert**: MCOClusterOperatorDown
- **Condition**: ClusterOperator status=Degraded >5 minutes
- **Impact**: MCO not functioning, updates halted
- **Response**: Check operator pod, sub-component status
- **Runbook**: `oc get co machine-config -o yaml`

### Warning Alerts

**Alert**: MCOUpdateSlow
- **Condition**: Pool update duration >p95 threshold
- **Impact**: Slower than expected updates, may indicate resource constraints
- **Response**: Check node resources, network latency, image pull times

**Alert**: MCOHighUpdateFailureRate
- **Condition**: >5% of updates failing
- **Impact**: Potential systemic issue causing update failures
- **Response**: Check common failure patterns in daemon logs

## Runbooks

### Node Update Failure

**Symptoms**: Daemon reports Degraded, node not reaching Updated=true

**Diagnosis**:
1. Check daemon logs: `oc logs -n openshift-machine-config-operator <daemon-pod>`
2. Check node conditions: `oc describe node <node>`
3. Check rendered config: `oc get mc rendered-<pool>-<hash> -o yaml`
4. Check daemon state: `oc debug node/<node> -- cat /host/run/machine-config-daemon-state.json`

**Resolution**:
- If rpm-ostree failure: Check disk space, image availability
- If file write failure: Check filesystem permissions, SELinux denials
- If systemd unit failure: Check unit validity, dependencies

### Pool Stuck in Updating

**Symptoms**: MachineConfigPool status.updating=true for extended period

**Diagnosis**:
1. Check pool status: `oc describe machineconfigpool <pool>`
2. Check individual node status: `oc get nodes -l node-role.kubernetes.io/<role>= -o wide`
3. Check daemon progress: `oc get pods -n openshift-machine-config-operator -l k8s-app=machine-config-daemon`
4. Check for drain failures: `oc get events --field-selector reason=FailedDrain`

**Resolution**:
- If maxUnavailable blocking: Wait for current nodes to complete
- If node draining failing: Check PodDisruptionBudgets, eviction errors
- If node update hanging: Check specific node daemon logs

## Incident Response

1. **Detection**: Alerts fire, ClusterOperator degraded, user reports
2. **Triage**: Check ClusterOperator status, component logs, pool status
3. **Mitigation**: Pause updates (`oc patch mcp <pool> -p '{"spec":{"paused":true}}'`), rollback if needed
4. **Resolution**: Fix root cause, resume updates, verify success
5. **Post-mortem**: Document incident, update runbooks, file improvements

## Capacity Planning

**Current Capacity**: 
- Supports 1000+ node clusters
- Update throughput limited by maxUnavailable and reboot time

**Growth Rate**: Cluster size growth depends on customer deployments

**Bottlenecks**:
- Node reboot time (~5-10 min)
- rpm-ostree image download time (network-dependent)
- API server rate limits (for very large clusters)

## Disaster Recovery

### Backup
**What's backed up**: MachineConfig and MachineConfigPool objects (part of cluster etcd backup)
**Frequency**: Continuous (etcd replication)
**Location**: etcd cluster

### Recovery Time Objective (RTO)
**Target**: < 1 hour to restore MCO functionality
**Procedure**: Restore etcd backup, redeploy MCO operator

### Recovery Point Objective (RPO)
**Target**: < 5 minutes of configuration changes
**Method**: etcd replication and backup

### Recovery Procedure
1. Restore etcd from backup
2. Verify MCO operator pod running
3. Verify sub-components (controller, server, daemons) running
4. Check ClusterOperator status
5. Verify pool status and node configurations

## Related Documentation

- [Architecture](./ARCHITECTURE.md) - Component interactions
- [Development](./DEVELOPMENT.md) - Debugging guides
- [Metrics Catalog](./generated/metrics-catalog.md) - Full metrics list (to be generated)
