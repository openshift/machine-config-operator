# vSphere boot image credentials

The Machine Config Operator (MCO) needs vCenter credentials when it reconciles
boot images for vSphere Machine API `MachineSet` objects. The release payload
includes a `CredentialsRequest` named
`openshift-machine-config-operator-vsphere` whose target is the following
Secret:

```text
openshift-machine-config-operator/vsphere-cloud-credentials
```

The boot image controller reads only this Secret. It does not fall back to the
installation credential in `kube-system/vsphere-creds`.

## When the Secret is required

On vSphere, boot image reconciliation is supported for Machine API `MachineSet`
objects and is enabled by default. It is an opt-out feature; it is not gated by
a feature gate. An administrator can explicitly select all MachineSets with the
following configuration:

```yaml
apiVersion: operator.openshift.io/v1
kind: MachineConfiguration
metadata:
  name: cluster
spec:
  managedBootImages:
    machineManagers:
      - apiGroup: machine.openshift.io
        resource: machinesets
        selection:
          mode: All
```

Setting the Machine API MachineSet manager's selection mode to `None`, or using
an empty `machineManagers` list, opts out. The credentials Secret is required
whenever vSphere MachineSets are enrolled for boot image reconciliation. Control
plane MachineSet boot image reconciliation is not supported on vSphere.

The `CredentialsRequest` supplies credentials; it does not enable boot image
reconciliation.

## Provisioning the Secret in Manual mode

When the Cloud Credential Operator runs in Manual mode, an administrator or
installation tooling must create the target Secret. Installer automation for
this MCO-specific Secret is separate follow-up work. Until that integration is
available, verify that installation manifests contain an equivalent Secret.

The Secret contains one username and password pair for every vCenter used by the
cluster. Each key is prefixed with the exact vCenter server name:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vsphere-cloud-credentials
  namespace: openshift-machine-config-operator
type: Opaque
stringData:
  vcenter.example.com.username: <username>
  vcenter.example.com.password: <password>
```

For multiple vCenters, add another `<vcenter>.username` and `<vcenter>.password`
pair for each server. Keep this Secret for as long as boot image reconciliation
is enabled. Rotate credentials by updating the same Secret.

For a new cluster, include the Secret in the installation manifests or create it
as soon as the cluster API is available. It must be present before the cluster
first reaches a completed installation state and begins boot image
reconciliation.

## Preparing for an upgrade

Before a minor upgrade of a cluster that uses manually maintained credentials,
extract the target release's vSphere `CredentialsRequest` objects:

```console
$ oc adm release extract --credentials-requests --cloud=vsphere \
    --to=<credentials-request-directory> <target-release-image>
```

Review the extracted request and ensure that
`openshift-machine-config-operator/vsphere-cloud-credentials` exists with valid
credentials before marking the Manual-mode credentials as prepared. This is
especially important when upgrading from a release that did not contain the MCO
request. After processing all CredentialsRequests for the target release, mark
the credentials prepared for the target minor version:

```console
$ oc patch cloudcredential.operator.openshift.io/cluster --type=merge \
    --patch '{"metadata":{"annotations":{"cloudcredential.openshift.io/upgradeable-to":"<target-minor-version>"}}}'
```

For example, use `4.21` as `<target-minor-version>` when preparing to upgrade to
an OpenShift 4.21 release. Start the upgrade only after creating the Secret and
applying this annotation.

The boot image controller defers reconciliation while the `ClusterVersion` is
installing or upgrading. It reconciles after the latest update history entry
changes to `Completed`. The Secret must therefore be ready before the upgrade
reaches that state. Preparing it before approving or starting the upgrade avoids
a post-upgrade boot image reconciliation failure.

## Failure behavior

If the Secret is absent, the controller reports a failure to read
`openshift-machine-config-operator/vsphere-cloud-credentials`. Missing keys are
passed as empty credential values. If vCenter rejects those values, or the
configured credentials cannot authenticate, vSphere client creation fails and
the error is propagated. These errors set the `MachineConfiguration` boot image
update condition to `Degraded`. With automatic boot image skew enforcement, a
degraded boot image controller also makes the Machine Config Operator report
that the cluster is not upgradeable until the failure is corrected.

The controller watches the dedicated Secret. Creating it, changing its
credential data, or deleting it immediately enqueues boot image reconciliation;
changes to unrelated Secrets do not. After valid credentials are supplied and
vSphere reconciliation succeeds, the controller clears the degraded condition.
Automatic boot image skew enforcement can then allow the Machine Config Operator
to report the cluster as upgradeable again. The controller never reads the root
installation credential as a fallback.

## vCenter account permissions

The vSphere `CredentialsRequest` uses an empty `VSphereProviderSpec` because the
current vSphere Cloud Credential Operator path copies administrator-managed
credentials; it does not create a vCenter account or grant vCenter privileges.
The empty provider spec must not be interpreted as a minimum permission set.

The account stored in the Secret must already have the vCenter permissions
needed by the MCO's boot image operations, including reading inventory,
importing an OVA, creating and deleting the managed template VM, changing its
boot options, marking it as a template, renaming it, and attaching tags. This
document does not prescribe an unverified minimum vSphere privilege list.

## Source references

- [Boot image platform support](../pkg/controller/common/featuregates.go)
- [Managed boot image selection](../pkg/operator/sync.go)
- [Upgrade timing and failure propagation](../pkg/controller/bootimage/boot_image_controller.go)
- [vSphere Secret lookup](../pkg/controller/bootimage/platform_helpers.go)
- [vSphere Secret data keys and operations](../pkg/controller/bootimage/vsphere_helpers.go)
- [Cloud Credential Operator Manual mode](https://github.com/openshift/cloud-credential-operator/blob/master/docs/mode-manual-creds.md)
