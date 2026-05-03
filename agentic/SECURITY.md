# Security - machine-config-operator

## Security Model

### Trust Boundaries

```
[Cluster Admin/Operator] → [Kubernetes API] → [MCO Components] → [Node OS]
  ^authenticated            ^RBAC enforced    ^privileged       ^root access
```

**Trust Zones**:
- **Control Plane**: MCO operator, controller, server (cluster-admin equivalent)
- **Data Plane**: MachineConfigDaemon (runs as root on nodes)
- **Configuration**: MachineConfig objects (cluster-admin write, all read)

### Threat Model

**Assets**:
1. **Node Operating Systems** - Direct control of OS configuration, kernel, systemd
2. **Cluster Secrets** - Pull secrets, certificates, keys
3. **Configuration Data** - MachineConfig objects containing files, units, settings

**Threats**:
1. **Malicious MachineConfig Injection**:
   - **Attack Vector**: Unauthorized user creates MachineConfig with malicious content
   - **Impact**: Arbitrary code execution on nodes, data exfiltration, cluster compromise
   - **Mitigation**: RBAC restricts MachineConfig creation to cluster-admins only
   - **Risk Level**: High (requires cluster-admin compromise)

2. **Supply Chain Attack on OS Images**:
   - **Attack Vector**: Compromised osImageURL pointing to malicious container image
   - **Impact**: Malicious OS deployed to all nodes in pool
   - **Mitigation**: Image signing, digest-based references (SHA256)
   - **Risk Level**: High (requires cluster-admin or image registry compromise)

3. **Daemon Privilege Escalation**:
   - **Attack Vector**: Exploit in MachineConfigDaemon code allowing container escape
   - **Impact**: Node compromise, lateral movement
   - **Mitigation**: Regular security updates, minimal dependencies, SELinux confinement
   - **Risk Level**: Medium (requires daemon vulnerability)

4. **Ignition Config Tampering**:
   - **Attack Vector**: MITM attack on first-boot Ignition fetch
   - **Impact**: Malicious first-boot configuration
   - **Mitigation**: HTTPS with certificate validation, embedded CA bundle
   - **Risk Level**: Low (requires network compromise during provisioning)

**Threat Modeling Framework**: STRIDE (Spoofing, Tampering, Repudiation, Information Disclosure, Denial of Service, Elevation of Privilege)

## Authentication & Authorization

### Authentication
**Mechanism**: Kubernetes ServiceAccount tokens (projected volumes)
**Implementation**: 
- Controller: pkg/controller/controller_context.go (uses in-cluster config)
- Daemon: pkg/daemon/daemon.go (uses node ServiceAccount)
**Token Lifetime**: Refreshed automatically by kubelet

### Authorization
**Model**: RBAC (Role-Based Access Control)
**Implementation**: manifests/machineconfigdaemon.yaml (ClusterRole, ClusterRoleBinding)

**Permissions**:
| Permission | Resource | Action | Who |
|------------|----------|--------|-----|
| machineconfigs | MachineConfig | get, list, watch | daemon, controller, operator |
| machineconfigs | MachineConfig | create, update, patch | controller, operator (renders) |
| machineconfigpools | MachineConfigPool | get, list, watch, update | controller, daemon |
| nodes | Node | get, list, watch, update (status) | daemon, controller |
| secrets | Secret | get (pull-secret) | controller, server |

**Note**: MachineConfig creation requires cluster-admin privileges (not granted to MCO components for normal operation).

## Data Protection

### Data Classification
- **Public**: MachineConfig structure (API schema)
- **Internal**: Node state, update progress
- **Confidential**: Rendered configs with embedded secrets
- **Restricted**: Pull secrets, SSH keys, TLS certificates

### Encryption
**At Rest**: 
- etcd encryption (cluster-wide setting, not MCO-specific)
- MachineConfig objects stored in etcd

**In Transit**: 
- HTTPS for Ignition serving (pkg/server/server.go uses TLS)
- Kubernetes API calls use TLS
- Cipher suites: Kubernetes defaults (TLS 1.2+)

**Key Management**: Kubernetes Secret objects, cluster-wide etcd encryption

### Secrets Management
**Storage**: Kubernetes Secrets (e.g., pull-secret in openshift-config namespace)
**Rotation**: Manual or automated via external secret manager
**Access Control**: RBAC limits Secret access to MCO components only

**Critical Secrets**:
- Pull secret: Used to fetch container images (OS, extensions)
- TLS certificates: Used by MachineConfigServer for HTTPS
- SSH keys: Embedded in MachineConfigs (user-provided)

## Input Validation

### MachineConfig Validation

**API Validation**: vendor/github.com/openshift/api/machineconfiguration/v1/types.go (OpenAPI schema)

**Content Validation**:
- Ignition config: Validated against Ignition v3.2.0 schema
- File paths: Must be absolute, must be in /etc or /var (writable locations)
- Systemd units: Basic syntax validation
- Kernel arguments: Validated against allowed patterns

**Location**: pkg/controller/common/helpers.go (ValidateMachineConfig)

### File Upload / Remote Content

**Size limits**: No explicit limit on Ignition config size (practical limit ~10MB)
**Type validation**: Remote files validated by content-type, checksums required
**Content scanning**: No antivirus scanning (assumes trusted sources)

**Embedded Content Fetching** (pkg/controller/template/render.go):
- HTTPS only (no HTTP)
- Certificate validation enforced
- Checksums required for remote files (Ignition verification field)

## Secure Coding Practices

### Mandatory Checks
- [ ] Input validation on all MachineConfig fields
- [ ] HTTPS for remote content fetching
- [ ] Checksum verification for remote files
- [ ] No shell injection in systemd unit execution
- [ ] Privilege minimization (drop caps where possible)

### Code Review Focus
- Authentication bypass risks: ServiceAccount token handling
- Authorization gaps: RBAC permissions, MachineConfig access control
- Injection vulnerabilities: Shell commands, file paths, Ignition templates
- Cryptographic misuse: TLS configuration, certificate validation
- Sensitive data exposure: Secrets in logs, rendered configs

## Vulnerability Management

### Dependency Scanning
**Tool**: Dependabot (GitHub), Snyk (optional)
**Frequency**: Daily
**Response SLA**: 
- Critical: 7 days
- High: 30 days
- Medium/Low: 90 days

### Security Testing
**SAST**: golangci-lint (basic security checks)
**DAST**: Not currently automated
**Penetration Testing**: Annual (Red Hat Product Security)

### Incident Response
**Security Incidents**:
1. **Detection**: CVE reports, security scanner alerts, bug reports
2. **Containment**: Hotfix release, advisory published
3. **Investigation**: Root cause analysis, affected versions identified
4. **Remediation**: Patch released, backported to supported versions
5. **Reporting**: CVE published, errata issued, customer notification

**Contact**: Red Hat Product Security (secalert@redhat.com)

## Compliance

**Standards**: 
- FIPS 140-2 (when spec.fips=true in MachineConfig)
- CIS Kubernetes Benchmark (cluster-level)
- SOC 2 (Red Hat OpenShift service)

**Audit Logs**: 
- Kubernetes audit logs capture all API calls (MachineConfig changes, Pool updates)
- Retention: Cluster-defined (typically 7-30 days)

**Compliance Checks**: 
- Node compliance via OpenShift Compliance Operator (separate component)
- Configuration drift detection: MachineConfigDaemon reconciliation

## Security Contacts

**Security Team**: Red Hat Product Security
**Vulnerability Reports**: https://access.redhat.com/security/team/contact
**Security Mailing List**: secalert@redhat.com

**For Responsible Disclosure**:
- Do NOT file public GitHub issues for security vulnerabilities
- Contact Red Hat Product Security directly
- Provide detailed reproduction steps, affected versions

## Related Documentation

- [Architecture](./ARCHITECTURE.md) - Component security boundaries
- [Design Philosophy](./DESIGN.md) - Security design principles
- [RBAC Manifests](../manifests/) - Actual RBAC policies
