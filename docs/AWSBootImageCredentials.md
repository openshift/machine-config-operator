# AWS boot image credentials

The Machine Config Operator (MCO) needs AWS credentials when it reconciles boot
images for AWS machine resources. The only AWS API action it calls is
`ec2:DescribeImages`, which it uses to classify the current AMI and to find
marketplace AMIs. The release payload includes a `CredentialsRequest` named
`openshift-machine-config-operator-aws` whose target is the following Secret:

```text
openshift-machine-config-operator/aws-cloud-credentials
```

The boot image controller reads only this Secret. It does not fall back to the
Machine API credentials in `openshift-machine-api/aws-cloud-credentials`.

## When the Secret is required

On AWS, boot image reconciliation is enabled by default for Machine API
`MachineSet` objects and is opt-in for `ControlPlaneMachineSet` objects. Cluster
API AWS machine templates are reconciled only when the corresponding feature
gates are enabled. The Secret is required whenever any of these resources are
enrolled for boot image reconciliation. The `CredentialsRequest` supplies
credentials; it does not enable boot image reconciliation.

## Permissions and service account

The `CredentialsRequest` grants exactly one permission:

```yaml
statementEntries:
- effect: Allow
  action:
  - ec2:DescribeImages
  resource: "*"
```

`ec2:DescribeImages` does not support resource-level permissions, so the
resource is `"*"`.

The request lists the `machine-config-controller` service account. When
credentials are provisioned with `ccoctl`, the IAM role trust policy is derived
from the Secret namespace and this list, so only the following subject can
assume the role:

```text
system:serviceaccount:openshift-machine-config-operator:machine-config-controller
```

On AWS, the `machine-config-controller` deployment mounts a projected service
account token with the `openshift` audience at
`/var/run/secrets/openshift/serviceaccount/token`. This is the token path that
`ccoctl` writes into the Secret. The token is not mounted on other platforms.
The AWS SDK reads the token file again on every credential refresh, so token
rotation does not require a restart.

## Supported Secret formats

The controller accepts the formats that the Cloud Credential Operator (CCO) and
`ccoctl` produce:

- A `credentials` key containing an INI document whose `[default]` section sets
  `role_arn` and `web_identity_token_file`. `ccoctl` writes this format for STS.
- Top-level `aws_access_key_id` and `aws_secret_access_key` keys together with a
  `credentials` key whose `[default]` section sets the same values. CCO writes
  this format in Mint and Passthrough modes.
- Manually created Secrets that use either top-level keys or the `credentials`
  key with the same key names.

STS web identity credentials take precedence over static keys, and the
`[default]` section of the `credentials` key takes precedence over top-level
keys. A partially configured pair, such as `role_arn` without
`web_identity_token_file`, is rejected. `web_identity_token_file` must be an
absolute path to a regular file; symbolic links are followed, so the projected
token layout is accepted.

The `credentials` parser supports only the subset that CCO and `ccoctl` write:
section headers, `key = value` lines, blank lines, and full-line comments that
start with `#` or `;`. Other profiles, inline comments, quoted values,
continuation lines, `source_profile`, `credential_process`, and SSO settings are
not supported.

## Provisioning the Secret

### Mint and Passthrough modes

CCO creates and maintains the Secret. No action is required, including when a
cluster is upgraded from a release that did not contain this request.

### Manual mode with static credentials

An administrator must create the Secret with credentials for an IAM identity
that is allowed to call `ec2:DescribeImages`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aws-cloud-credentials
  namespace: openshift-machine-config-operator
type: Opaque
stringData:
  aws_access_key_id: <access-key-id>
  aws_secret_access_key: <secret-access-key>
```

Rotate credentials by updating the same Secret.

### Manual mode with STS

For a new cluster, the request is processed together with the other release
`CredentialsRequest` objects. Extract them from the release image and run
`ccoctl aws create-all` or `ccoctl aws create-iam-roles` as described in the
CCO documentation, then include the generated manifests in the installation
manifests. For this request, `ccoctl` generates the IAM role and the Secret
manifest
`openshift-machine-config-operator-aws-cloud-credentials-credentials.yaml`.

## Preparing for a minor upgrade

Before a minor upgrade of a cluster that uses Manual mode, extract the target
release's AWS `CredentialsRequest` objects:

```console
$ oc adm release extract --credentials-requests --cloud=aws \
    --to=<credentials-request-directory> <target-release-image>
```

When upgrading from a release that did not contain
`openshift-machine-config-operator-aws`, provision its Secret before starting
the upgrade:

- With static credentials, create the Secret shown above.
- With STS, create the IAM role and Secret manifest with `ccoctl`, then apply
  the generated Secret:

  ```console
  $ ccoctl aws create-iam-roles --name=<name> --region=<aws-region> \
      --credentials-requests-dir=<credentials-request-directory> \
      --identity-provider-arn=<oidc-provider-arn> --output-dir=<output-directory>
  $ oc apply -f <output-directory>/manifests/openshift-machine-config-operator-aws-cloud-credentials-credentials.yaml
  ```

After processing all `CredentialsRequest` objects for the target release, mark
the credentials prepared for the target minor version:

```console
$ oc patch cloudcredential.operator.openshift.io/cluster --type=merge \
    --patch '{"metadata":{"annotations":{"cloudcredential.openshift.io/upgradeable-to":"<target-minor-version>"}}}'
```

Alternatively, `ccoctl aws set-upgradeable-to <target-minor-version>` sets the
same annotation. In Manual mode, CCO reports `Upgradeable=False` until this
annotation names a minor version newer than the current one. The annotation is
an administrator acknowledgement; CCO does not check that the Secrets exist.

The boot image controller defers reconciliation while the `ClusterVersion` is
installing or upgrading, and reconciles after the latest update history entry
changes to `Completed`. Preparing the Secret before starting the upgrade avoids
a boot image reconciliation failure after the upgrade.

## Failure behavior

The controller reports an error when the Secret is absent, when it does not
contain a complete set of supported keys, or when the STS token file cannot be
opened or is not a regular file. Errors name the Secret and the keys involved
but never include credential values. The token file is checked when the EC2
client is built; later problems surface as AWS errors when credentials are
refreshed. These
errors set the `MachineConfiguration` boot image update condition to
`Degraded`. With automatic boot image skew enforcement, a degraded boot image
controller also makes the MCO report that the cluster is not upgradeable until
the failure is corrected.

The controller watches the dedicated Secret. Creating it, changing its data, or
deleting it immediately enqueues boot image reconciliation; changes to
unrelated Secrets and metadata-only changes do not. A changed Secret causes a
new EC2 client to be built. After valid credentials are supplied and
reconciliation succeeds, the controller clears the degraded condition.

## Limitations

- Only the `[default]` profile and the formats listed above are supported.
- Environments that provision operator IAM roles outside `ccoctl` must also
  provision a role and Secret for this request. That integration is outside
  the scope of this document.

## Source references

- [Boot image platform support](../pkg/controller/common/featuregates.go)
- [AWS Secret lookup and client creation](../pkg/controller/bootimage/aws_helpers.go)
- [Secret formats and parsing](../pkg/controller/bootimage/aws_credentials.go)
- [Secret watch and upgrade timing](../pkg/controller/bootimage/boot_image_controller.go)
- [CredentialsRequest](../install/0000_80_machine-config_01_credentials-request-aws.yaml)
- [Controller deployment](../manifests/machineconfigcontroller/deployment.yaml)
- [Cloud Credential Operator Manual mode](https://github.com/openshift/cloud-credential-operator/blob/master/docs/mode-manual-creds.md)
- [Cloud Credential Operator short-term credentials](https://github.com/openshift/cloud-credential-operator/blob/master/docs/short-term-credentials.md)
- [ccoctl usage](https://github.com/openshift/cloud-credential-operator/blob/master/docs/ccoctl.md)
