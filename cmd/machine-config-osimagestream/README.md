# machine-config-osimagestream

A CLI tool for retrieving OSImageStream information from OpenShift/OKD release payload images or ImageStream files.

## Overview

The `machine-config-osimagestream` tool is part of the Machine Config Operator and provides utilities for extracting OS image information from release payloads. It supports two main commands:

1. **get osimagestream** - Retrieve the full OSImageStream resource
2. **get default-node-image** - Get the default node OS image pullspec

This tool is useful for automation, debugging, and understanding which OS images are available in a given OpenShift/OKD release.

## Basic Usage

All commands require authentication to pull from image registries. Use the `--authfile` flag to provide credentials.

### Get OSImageStream

Retrieve the complete OSImageStream resource:

```bash
# From release image (outputs JSON by default)
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# From ImageStream file
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --imagestream /path/to/imagestream.json
```

### Get Default Node OS Image Pullspec

Retrieve the default node OS image pullspec:

```bash
# Get the default node OS image pullspec from release image
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# Get the default node OS image pullspec from ImageStream file
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --imagestream /path/to/imagestream.json
```

## Flag Reference

### Required Flags

#### `--authfile` (string)
Path to an image registry authentication file (pull secret). This is required for pulling from authenticated registries.

**Example:**
```bash
--authfile /path/to/pull-secret.json
```

The authentication file should be in Docker config.json format:
```json
{
  "auths": {
    "registry.example.com": {
      "auth": "base64encodedstring"
    }
  }
}
```

### Input Source Flags (Mutually Exclusive)

You must provide exactly one of these flags:

#### `--release-image` (string)
The OCP/OKD release payload image to run against.

**Example:**
```bash
--release-image quay.io/openshift-release-dev/ocp-release:latest
```

#### `--imagestream` (string)
Path to an ImageStream file (JSON or YAML format) to run against.

**Example:**
```bash
--imagestream /path/to/imagestream.json
```

### Certificate and Authentication Flags (Optional)

#### `--cert-dir` (string)
Path to a directory containing certificates for verifying registry connections.

**Example:**
```bash
--cert-dir /etc/docker/certs.d
```

#### `--per-host-certs` (string)
Path to per-host certificates directory (similar to `/etc/containers/certs.d`).

**Example:**
```bash
--per-host-certs /etc/containers/certs.d
```

#### `--trust-bundle` (string, repeatable)
Path(s) to additional trust bundle file(s) or directory(ies). Can be specified multiple times to include multiple trust bundles.

**Examples:**
```bash
# Single trust bundle file
--trust-bundle /etc/pki/ca-trust/source/anchors/ca-bundle.crt

# Multiple trust bundles
--trust-bundle /etc/pki/ca-trust/source/anchors/ \
--trust-bundle /opt/custom/cert.pem
```

#### `--registry-config` (string)
Path to a registries.conf file for configuring registry access.

**Example:**
```bash
--registry-config /etc/containers/registries.conf
```

### Output Flags (for `get osimagestream` only)

#### `--output-format` (string, default: "json")
The output format to use: `json` or `yaml`.

**Examples:**
```bash
--output-format json   # Default
--output-format yaml
```

#### `--output-file` (string)
Path to write output to a file instead of stdout. The format is auto-detected from the file extension (`.json`, `.yaml`, `.yml`). If the file has no extension, uses `--output-format`.

**Examples:**
```bash
# Auto-detect JSON format from extension
--output-file /tmp/osimagestream.json

# Auto-detect YAML format from extension
--output-file /tmp/osimagestream.yaml

# No extension, uses --output-format flag
--output-file /tmp/osimagestream --output-format yaml
```

## Detailed Examples

### get osimagestream

#### Basic Examples

```bash
# Get OSImageStream from release image (JSON to stdout)
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# Get OSImageStream from ImageStream file
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --imagestream /path/to/imagestream.json

# Output in YAML format
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  --output-format yaml
```

#### File Output Examples

```bash
# Write JSON to file (auto-detected from .json extension)
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  --output-file /tmp/osimagestream.json

# Write YAML to file (auto-detected from .yaml extension)
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  --output-file /tmp/osimagestream.yaml

# Write to file with no extension (uses --output-format)
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  --output-format yaml \
  --output-file /tmp/osimagestream
```

#### Certificate Examples

```bash
# With custom certificate directory
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --cert-dir /etc/docker/certs.d \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With per-host certificates
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --per-host-certs /etc/containers/certs.d \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With additional trust bundle
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --trust-bundle /etc/pki/ca-trust/source/anchors/ca-bundle.crt \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With multiple trust bundles
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --trust-bundle /etc/pki/ca-trust/source/anchors/ \
  --trust-bundle /opt/custom/cert.pem \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With registry configuration
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --registry-config /etc/containers/registries.conf \
  --release-image quay.io/openshift-release-dev/ocp-release:latest
```

#### Combined Examples

```bash
# All certificate options together with YAML output to file
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --cert-dir /etc/docker/certs.d \
  --per-host-certs /etc/containers/certs.d \
  --trust-bundle /etc/pki/ca-trust/source/anchors/ca-bundle.crt \
  --registry-config /etc/containers/registries.conf \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  --output-file /tmp/osimagestream.yaml
```

### get default-node-image

#### Basic Examples

```bash
# Get default node image from release image
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# Get default node image from ImageStream file
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --imagestream /path/to/imagestream.json
```

#### Certificate Examples

```bash
# With custom certificate directory
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --cert-dir /etc/docker/certs.d \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With per-host certificates
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --per-host-certs /etc/containers/certs.d \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With additional trust bundle
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --trust-bundle /etc/pki/ca-trust/source/anchors/ca-bundle.crt \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With multiple trust bundles
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --trust-bundle /etc/pki/ca-trust/source/anchors/ \
  --trust-bundle /opt/custom/cert.pem \
  --release-image quay.io/openshift-release-dev/ocp-release:latest

# With registry configuration
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --registry-config /etc/containers/registries.conf \
  --release-image quay.io/openshift-release-dev/ocp-release:latest
```

#### Combined Examples

```bash
# All certificate options together
machine-config-osimagestream get default-node-image \
  --authfile /path/to/pull-secret.json \
  --cert-dir /etc/docker/certs.d \
  --per-host-certs /etc/containers/certs.d \
  --trust-bundle /etc/pki/ca-trust/source/anchors/ca-bundle.crt \
  --registry-config /etc/containers/registries.conf \
  --release-image quay.io/openshift-release-dev/ocp-release:latest
```

## Environment Variables

The tool respects standard HTTP proxy environment variables:

- `HTTP_PROXY` - HTTP proxy URL
- `HTTPS_PROXY` - HTTPS proxy URL
- `NO_PROXY` - Comma-separated list of hosts to exclude from proxying

**Example:**
```bash
export HTTP_PROXY=http://proxy.example.com:8080
export HTTPS_PROXY=http://proxy.example.com:8080
export NO_PROXY=localhost,127.0.0.1,.example.com

machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest
```

### Input Source Selection

Always provide exactly one of `--release-image` or `--imagestream`:
- Use `--release-image` to pull from a remote registry
- Use `--imagestream` to read from a local file (faster, no network required)

### Certificate Handling

Certificate flags are optional but may be required for:
- Self-signed certificates
- Corporate proxies
- Custom CA bundles
- Private registries

The `--trust-bundle` flag can be specified multiple times to include multiple certificate sources.

### Output Format

For `get osimagestream`:
- Default output is JSON to stdout
- Use `--output-format yaml` for YAML output
- Use `--output-file` to write to a file (format auto-detected from extension)
- File extension takes precedence over `--output-format`

For `get default-node-image`:
- Output is plain text (the image pullspec)
- Use shell redirection to save to a file: `> output.txt`

### Performance Considerations

- Reading from `--imagestream` files is faster than pulling from `--release-image`
- If you need to query the same release multiple times, consider saving the ImageStream to a file first
- Network timeouts are set to 5 minutes

### Debugging

Add `-v=4` or higher for verbose logging:

```bash
machine-config-osimagestream get osimagestream \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  -v=4
```
