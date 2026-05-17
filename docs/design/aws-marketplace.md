# AWS Marketplace RHCOS Boot Image Updates (MCO)

## Overview

This document describes how the Machine Config Operator (MCO) resolves the correct RHCOS AMI when updating nodes that were provisioned from an AWS Marketplace image.

AWS Marketplace offers RHCOS images under several OpenShift product tiers. Each tier has a distinct product ID embedded in the AMI name. The MCO uses this product ID as a stable identifier to find the updated AMI for a given release, without requiring any stored state about which offering the cluster was originally installed from.

## Flavors

Each offering has a unique product ID per architecture embedded in the AMI name, e.g.:

```
RHEL-9.4-RHCOS-4.18_HVM_GA-20251119-x86_64-0-59ead7de-2540-4653-a8b0-fa7926d5c845
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

This feature targets OCP 4.22+, where RHCOS uses RHEL-aligned versioning. Marketplace AMIs follow this naming convention:

```
RHEL-{rhel-version}-RHCOS-{version-token}_HVM_GA-{date}-{arch}-{index}-{uuid}
```

The `{version-token}` is derived from the leading two segments of the RHCOS release string in the stream configmap:

| RHCOS release string (configmap) | Version token in AMI name |
|---|---|
| `9.6.20260210-0` | `9.6` |

Pre-4.19 clusters used a different RHCOS release string format (`418.94.202511191518-0`) and a different AMI naming scheme that predates marketplace boot image support. This enhancement would only land in 4.22+, so this format is not considered while parsing the configmap's release version value.

## AMI Description Field

Each marketplace AMI carries a `Description` field that embeds the version token. Two formats are in use depending on the product line:

| Product line | Description format | Example |
|---|---|---|
| OCP / OKE / OPP (RHEL marketplace) | `RHEL CoreOS {N.M} {release-string} {arch}` | `RHEL CoreOS 9.6 9.6.20260210-0 x86_64` |
| ROSA | `rhcos-{N.M}.{date}-{index}-{arch}` | `rhcos-9.6.20250701-0-x86_64` |

The version token (e.g. `9.6`) is the primary field used for version matching at runtime. Because the surrounding characters differ between formats (spaces for RHEL marketplace, dash/dot for ROSA), the MCO checks both boundaries when filtering results.

Marketplace AMIs are distinguished from standard RHCOS AMIs by owner account `679593333241` (`aws-marketplace`). Standard RHCOS AMIs are owned by `531415883065` and have no UUID in their name.

## AMI Detection

All AWS boot image updates begin with a `DescribeImages` call on the MachineSet's current AMI ID. The MCO branches on `OwnerId`:

- `531415883065` (Red Hat) → standard RHCOS — update target comes from stream configmap by region and architecture
- `679593333241` (AWS Marketplace) → marketplace RHCOS — use the marketplace resolution flow below
- anything else → custom/unknown image, skip and log

## Runtime Flow

### Standard path

The target AMI ID is read directly from the stream configmap by region and architecture. No additional EC2 API calls are needed beyond the initial `DescribeImages` on the current AMI.

### Marketplace path

AMI IDs are region-scoped. The MCO resolves the correct marketplace AMI at update time using a `DescribeImages` name-pattern filter anchored to the product ID. No stored flavor state is required.

#### Step 1: extract the product ID

The current AMI is already known from the MachineSet's provider spec. Its name contains the product ID as the trailing segment:

```
RHEL-9.4-RHCOS-4.18_HVM_GA-20251119-x86_64-0-{product-id}
```

Extract the product ID by splitting on `-` and taking the last five groups, then validating the result as a UUID (`8-4-4-4-12` hex format).

#### Step 2: derive the target version token

From the stream configmap's RHCOS release string for the target OCP version:

```
9.6.20260210-0  →  token "9.6"
```

Take the first two dot-separated segments.

#### Step 3: find the updated AMI

Call `DescribeImages` in the node's region:

```
--owners aws-marketplace
--filters "Name=name,Values=*{product-id}*"
```

From the results, filter to images whose `Description` field contains the version token derived in step 2, matching against both description formats (space-bounded for RHEL marketplace, dash/dot-bounded for ROSA). Among those, select the AMI with the latest `CreationDate`.

If no matching AMI is found in the region (e.g. replication lag), the MCO skips and retries on the next reconcile cycle rather than falling back to a different version.

### Total EC2 calls per MachineSet reconcile

- **All AWS clusters**: 1 `DescribeImages` call on the current AMI (owner-based classification)
- **Marketplace clusters** (additional): 1 `DescribeImages` call with the product ID filter

## Credentials

The MCO reads AWS credentials from the `aws-cloud-credentials` secret in the `openshift-machine-api` namespace. This secret is provisioned by the machine-api-operator's `CredentialsRequest` and already includes `ec2:DescribeImages` with `resource: "*"`. No new `CredentialsRequest` or RBAC changes are required — the MCO's existing clusterrole grants cluster-wide secret read access.
