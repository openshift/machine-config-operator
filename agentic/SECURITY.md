# Security - machine-config-operator

## Security Model

### Trust Boundaries

```
[Cluster Admin] → [Kubernetes API] → [MCO/MCC] → [Node Annotations] → [MCD (privileged)]
  ^untrusted         ^auth/RBAC      ^trusted     ^authenticated    ^full node access

[New Node] → [MCS (TLS client cert required)] → [Ignition Config]
  ^untrusted    ^authenticated                  ^bootstrapping
```

### Threat Model

**Assets**:
1. **Node OS Configuration**: Files, systemd units, OS state
   - Protection: RBAC on MachineConfig, config drift detection, validated Ignition
2. **Cluster Control Plane**: API server, etcd on masters
   - Protection: Controlled rollout (maxUnavailable), drain before reboot, etcd quorum checks
3. **Workload Data**: Applications running on nodes
   - Protection: Proper drain, PodDisruptionBudget respect, gradual rollout

**Threats**:

1. **Threat**: Unauthorized MachineConfig creation/modification
   - **Attack Vector**: Compromised user account with cluster-admin privileges
   - **Impact**: Arbitrary code execution on all nodes via malicious config
   - **Mitigation**: RBAC limits MachineConfig access to cluster-admins, audit logging of all changes
   - **Risk Level**: High

2. **Threat**: Man-in-the-middle on Ignition delivery (bootstrap)
   - **Attack Vector**: Network interception during new node provisioning
   - **Impact**: Malicious Ignition config delivered to new node
   - **Mitigation**: TLS with client certificate authentication for MCS, node authenticates to cluster
   - **Risk Level**: Medium (requires network access during bootstrap)

3. **Threat**: Config drift attack (unauthorized file modification)
   - **Attack Vector**: SSH access to node + manual file edits
   - **Impact**: Backdoor, malware, or misconfiguration on individual nodes
   - **Mitigation**: Config drift detection marks node Degraded within seconds, blocks further updates
   - **Risk Level**: Medium (requires node access)

4. **Threat**: Malicious OS image
   - **Attack Vector**: Compromise of OS image registry or man-in-the-middle
   - **Impact**: Entire cluster runs compromised OS
   - **Mitigation**: OSImageURL is digested (sha256), pulled over HTTPS, verified by rpm-ostree
   - **Risk Level**: Low (requires registry compromise + breaking digest verification)

**Threat Modeling Framework**: STRIDE (Spoofing, Tampering, Repudiation, Information Disclosure, Denial of Service, Elevation of Privilege)

## Authentication & Authorization

### Authentication

**Mechanism**:
- **API access**: Kubernetes ServiceAccount tokens (MCO, MCC, MCD, MCS each have dedicated ServiceAccounts)
- **Node bootstrap**: TLS client certificates (nodes authenticate to MCS)

**Implementation**:
- ServiceAccounts: `manifests/*/sa.yaml`
- Bootstrap tokens: `manifests/machineconfigserver/node-bootstrapper-token.yaml`

**Token Lifetime**:
- ServiceAccount tokens: Managed by Kubernetes (rotated automatically)
- Bootstrap tokens: Short-lived, deleted after cluster bootstrap complete

### Authorization

**Model**: Kubernetes RBAC (Role-Based Access Control)

**Implementation**:
- ClusterRoles/ClusterRoleBindings for MCO components
- Least privilege: Each component has only permissions it needs

**Key Permissions**:

| Component | Resources | Verbs | Why |
|-----------|-----------|-------|-----|
| MCO | Deployments, DaemonSets | get, list, watch, create, update | Manages component lifecycle |
| MCC | MachineConfigs, MachineConfigPools | get, list, watch, update | Renders configs, updates pools |
| MCD | Nodes | get, list, watch, update | Updates node annotations, marks status |
| MCS | MachineConfigs, MachineConfigPools | get, list, watch | Serves configs to new nodes |

**User Access**:
- Creating/editing MachineConfig: Requires `cluster-admin` role (highly privileged)
- Viewing MachineConfig: Any authenticated user (configs are not secret)
- Deleting MachineConfig: Requires `cluster-admin` role

**RBAC Files**: `manifests/*/` directories contain ClusterRole and RoleBinding manifests

## Data Protection

### Data Classification

- **Public**: MachineConfig manifests (not secret, but sensitive)
- **Internal**: Node annotations, component logs
- **Confidential**: Pull secrets (in MachineConfig), SSH keys (if added)
- **Restricted**: Node-level secrets, etcd data

### Encryption

**At Rest**:
- etcd encryption: MachineConfigs stored in encrypted etcd (if cluster configured)
- Node disk: Not encrypted by MCO (can be enabled via machine-api-operator)

**In Transit**:
- Kubernetes API: TLS (API server)
- MCS → Nodes: HTTPS with client cert verification
- Registry pulls: HTTPS

**Key Management**:
- TLS certificates: Managed by OpenShift certificate operator
- Pull secrets: Stored in MachineConfig, synced to `/var/lib/kubelet/config.json`

### Secrets Management

**Storage**:
- Pull secrets: Kubernetes Secret, injected into MachineConfig by MCO
- SSH keys: Stored in MachineConfig (not recommended for production)

**Rotation**:
- Pull secret: Updated via `oc set data secret/pull-secret`
- TLS certs: Automatically rotated by cert-manager

**Access Control**:
- Pull secret: Readable by MCO (ServiceAccount)
- SSH keys: Readable by anyone who can read MachineConfigs (cluster-admin)

## Input Validation

**MachineConfig Validation**:
- Ignition config syntax validated by API server (OpenAPI schema)
- Additional validation by MCO admission webhook (if configured)
- File paths validated (must be absolute)
- Invalid configs rejected before persisting

**API Input**:
- All CRD fields validated by OpenAPI schema
- Ignition version checked (must be supported version)
- Field constraints enforced (e.g., maxUnavailable > 0)

**File Upload**: N/A (MCO does not accept file uploads)

## Secure Coding Practices

### Mandatory Checks

- [ ] Input validation on all MachineConfig fields
- [ ] Validate Ignition config before applying to nodes
- [ ] Sanitize file paths (prevent path traversal)
- [ ] No shell injection vulnerabilities (use exec.Command with args, not sh -c)
- [ ] Least privilege for ServiceAccounts

### Code Review Focus

- Shell command construction (avoid concatenating untrusted input)
- File path handling (prevent ../.. attacks)
- Ignition config parsing (malformed configs)
- RBAC correctness (no excessive permissions)
- Sensitive data exposure in logs

## Vulnerability Management

### Dependency Scanning

**Tool**: Dependabot (GitHub)
**Frequency**: Daily
**Response SLA**: Critical: 7 days, High: 30 days, Medium: 90 days

**Process**:
1. Dependabot opens PR with vulnerability fix
2. Review PR, run tests
3. Merge if tests pass
4. Backport to supported releases if needed

### Security Testing

**SAST**: golangci-lint (static analysis)
**DAST**: Not currently implemented
**Penetration Testing**: Annual (coordinated by Red Hat Product Security)

### Incident Response

**Security Incidents**:
1. **Detection**: Vulnerability reported via https://access.redhat.com/security/team/contact
2. **Triage**: Product Security team assesses severity
3. **Mitigation**: Develop fix, test, create CVE if needed
4. **Release**: Patch released in next z-stream
5. **Notification**: Security advisory published, customers notified

## Compliance

**Standards**: N/A (no specific compliance certifications for MCO itself)

**Audit Logs**:
- Kubernetes API audit logs track all MachineConfig changes
- Node-level changes logged via journald

**Compliance Checks**: N/A

## Security Contacts

**Security Team**: Red Hat Product Security
**Vulnerability Reports**: https://access.redhat.com/security/team/contact
**Security Mailing List**: N/A (use Product Security contact)

**For confidential issues**, do not file public GitHub issues. Use Red Hat Product Security contact above.

## Related Documentation

- [Design Philosophy](./DESIGN.md) - Security by design principles
- [Threat Model Details](./design-docs/) - Detailed threat analysis
- [RBAC Manifests](../manifests/) - RBAC configuration files
