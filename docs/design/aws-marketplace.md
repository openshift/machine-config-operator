# AWS Marketplace RHCOS Boot Image Updates (MCO)

## Overview

This document describes how the Machine Config Operator (MCO) resolves the correct RHCOS AMI when updating nodes that were provisioned from an AWS Marketplace image.

AWS Marketplace offers RHCOS images under several OpenShift product tiers. Each tier has a distinct product ID embedded in the AMI name. The MCO uses this product ID as a stable identifier to find the updated AMI for a given release, without requiring any stored state about which offering the cluster was originally installed from.

## Flavors

Each offering has a unique product ID per architecture embedded in the AMI name, e.g.:

```text
RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-59ead7de-2540-4653-a8b0-fa7926d5c845
```

### x86_64

| Offering | Product ID |
|---|---|
| OCP | `59ead7de-2540-4653-a8b0-fa7926d5c845` |
| OKE | `963b36c3-de6f-48ed-b802-2b38b2a2cdeb` |
| OPP | `f5da01a6-d046-487c-9072-42fe53b1cad4` |

### arm64

| Offering | Product ID |
|---|---|
| OCP | `abc249f8-7440-45f7-a4b1-c026baff64c1` |
| OKE | `d2d3ebcd-c1ca-43d8-bf0a-530433200f35` |
| OPP | `be6d3e94-c8dc-4a3e-9218-4b449b11f06f` |

### x86_64 EMEA (EU, Middle East, Africa)

| Offering | Product ID |
|---|---|
| OCP EMEA | `962791c7-3ae5-46d1-ba62-c7a5ebac54fd` |
| OKE EMEA | `7026c8d7-392c-4010-b93c-f93f7bc5495f` |
| OPP EMEA | `628c9df3-0254-4f91-bc1f-8619d1b8eaa8` |

EMEA has no arm64 variants.

### ROSA

| Offering | Product ID |
|---|---|
| ROSA | `34850061-abaf-402d-92df-94325c9e947f` |

ROSA Classic is planned to be sunset for 5.0, it was implemented because the solution was identical to the other flavors listed above.

## AMI Name Format

Marketplace AMIs for RHEL-aligned RHCOS follow this naming convention:

```text
RHEL-{rhel-version}-RHCOS-{rhcos-version}_HVM_GA-{date}-{arch}-{index}-{uuid}
```

The `{rhcos-version}` token is the RHEL-aligned RHCOS version derived from the leading two segments of the release string in the stream ConfigMap:

| RHCOS release string (ConfigMap) | Version token in AMI name |
|---|---|
| `9.6.20260210-0` | `9.6` |

Each architecture has its own AMI with the arch embedded in the name:

```text
RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-59ead7de-2540-4653-a8b0-fa7926d5c845
RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-aarch64-0-abc249f8-7440-45f7-a4b1-c026baff64c1
```

Note that x86_64 and arm64 AMIs carry **different product IDs** (see tables above), so each arch's listing is kept entirely separate.

### Mixed-format listings

A marketplace product listing may contain AMIs published under both the old OCP-based versioning scheme and the newer RHEL-aligned scheme. Old-format AMIs have a version token with a large major component (e.g. `418.94` for OCP 4.18); RHEL-aligned AMIs use small major versions (e.g. `9.6`). When querying for the latest AMI, the MCO parses the version token from every result and:

- Discards any AMI whose version is strictly greater than the target token (not yet safe to use)
- Separately detects old-format tokens (major > 100) and excludes them from consideration
- If only old-format AMIs are found in a region, emits a warning and skips the update, leaving the boot image skew in place so the operator is aware the image is out of date

## AMI Description Field

Each marketplace AMI carries a `Description` field that embeds the full RHCOS release string. Two formats are in use depending on the product line:

| Product line | Description format | x86_64 example | aarch64 example |
|---|---|---|---|
| OCP / OKE / OPP (RHEL marketplace) | `RHEL CoreOS {N.M} {release-string} {arch}` | `RHEL CoreOS 9.6 9.6.20260210-0 x86_64` | `RHEL CoreOS 9.6 9.6.20260210-0 aarch64` |
| ROSA | `rhcos-{release-string}-{arch}` | `rhcos-9.6.20250701-0-x86_64` | `rhcos-9.6.20250701-0-aarch64` |
| Pre-RHEL-aligned (old) | `OpenShift {ocp-version} {release-string} {arch}` | `OpenShift 4.18 418.94.202511191518-0 x86_64` | `OpenShift 4.18 418.94.202511191518-0 aarch64` |

The MCO extracts the full release string (e.g. `9.6.20260210-0`) from the description using a regex that matches the version pattern wherever it appears in the string — no format-specific boundary logic is required. The `N.M` token (e.g. `9.6`) is used for version comparison; the full release string is stored in `ClusterBootImageAutomatic.RHCOSVersion`.

Marketplace AMIs are distinguished from standard RHCOS AMIs by owner account `679593333241` (`aws-marketplace`). Standard RHCOS AMIs are owned by `531415883065` and have no UUID in their name.

## AMI Detection

All AWS boot image updates begin with a `DescribeImages` call on the MachineSet's current AMI ID. The MCO branches on `OwnerId`:

- `531415883065` (Red Hat) → standard RHCOS — update target comes from stream ConfigMap by region and architecture
- `679593333241` (AWS Marketplace) → marketplace RHCOS — use the marketplace resolution flow below
- anything else → custom/unknown image, skip and log

## Runtime Flow

### Standard path

The target AMI ID is read directly from the stream ConfigMap by region and architecture. No additional EC2 API calls are needed beyond the initial `DescribeImages` on the current AMI.

### Marketplace path

AMI IDs are region-scoped. The MCO resolves the correct marketplace AMI at update time using a `DescribeImages` name-pattern filter anchored to the product ID. No stored flavor state is required.

#### Step 1: extract the product ID

The current AMI is already known from the MachineSet's provider spec. Its name contains the product ID as the trailing segment:

```text
RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-{product-id}
```

Extract the product ID by splitting on `-` and taking the last five groups, then validating the result as a UUID (`8-4-4-4-12` hex format).

#### Step 2: derive the target version token

From the stream ConfigMap's RHCOS release string for the MachineSet's architecture:

```text
9.6.20260210-0  →  token "9.6"
```

Take the first two dot-separated segments. The lookup is per-arch — arm64 and x86_64 MachineSets each resolve their own release string from the stream ConfigMap.

#### Step 3: find the updated AMI

Call `DescribeImages` in the node's region:

```bash
--owners aws-marketplace
--filters "Name=name,Values=*{product-id}*"
```

From the results, parse the full release string and version token (`N.M`) out of each AMI's `Description` field. The regex matches the version pattern wherever it appears in the string and handles all three description formats (RHEL marketplace, ROSA, and pre-RHEL-aligned) without format-specific logic.

Candidates are filtered as follows:

1. AMIs with a token strictly greater than the target are discarded (not yet safe to use in this cluster)
2. AMIs with an old-format token (major > 100, e.g. `418.94`) are excluded and tracked separately
3. Among the remaining candidates, the AMI at the highest version is preferred; ties are broken by newest `CreationDate`

Accepting one version older than the target prevents stalling when the target version AMI has not yet replicated to this region. The selected AMI will never be ahead of the target stream. If no suitable AMI is found, the MCO skips the update and retries on the next reconcile cycle. If only old-format AMIs are found, a warning is emitted instead and the update is skipped without scheduling a retry.

### Total EC2 calls per MachineSet reconcile

- **All AWS clusters**: 1 `DescribeImages` call on the current AMI (owner-based classification)
- **Marketplace clusters** (additional): 1 `DescribeImages` call with the product ID filter

## Credentials

The MCO reads AWS credentials from the `aws-cloud-credentials` secret in the `openshift-machine-api` namespace. This secret is provisioned by the machine-api-operator's `CredentialsRequest` and already includes `ec2:DescribeImages` with `resource: "*"`. No new `CredentialsRequest` or RBAC changes are required — the MCO's existing clusterrole grants cluster-wide secret read access.

## E2E Testing

The E2E test validates that the MCO correctly identifies the product line of a marketplace AMI and updates it to the appropriate newer image. It runs on a standard (non-marketplace) AWS cluster by directly patching MachineSet AMIs to simulate marketplace-provisioned nodes.

### Test flow (repeated sequentially for each product ID)

For each known product ID (OCP, OKE, OPP, ROSA, and EMEA variants) for the cluster's architecture and region:

1. **Read the target version token** from the stream ConfigMap for the cluster's architecture.
2. **Query EC2** for all marketplace AMIs with the product ID whose version token is strictly less than the stream version token. Sort results descending by version.
3. **Select the second-newest** AMI from that list. Using the second-newest (rather than the newest) ensures the controller always has a strictly newer candidate to update to — the newest AMI below the stream version may already be selected by the controller as the best `≤` match, making the test a no-op.
4. **Patch the MachineSet** AMI to the selected AMI ID.
5. **Wait for the MCO to reconcile** the MachineSet.
6. **Assert** that the MachineSet AMI changed, the new AMI is owned by `679593333241` (`aws-marketplace`), and its name contains the same product ID as the original.

If fewer than two AMIs are found below the stream version for a given product ID in the cluster's region, that product ID is skipped with a log message.

### Why a standard cluster

A standard CI cluster is used rather than a marketplace-provisioned one. The MCO branches on `OwnerId` from `DescribeImages`, so patching a MachineSet to a real marketplace AMI ID is sufficient to trigger the marketplace reconciliation path — no special cluster provisioning is required.
