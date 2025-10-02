# mco-sanitize

A command-line tool that removes sensitive information from Machine Config Operator (MCO) must-gather reports while 
preserving their structure for debugging and analysis purposes.

## Overview

`mco-sanitize` is designed to sanitize OpenShift must-gather reports by redacting sensitive data from Kubernetes 
resources according to configurable rules. The tool maintains the original file structure and metadata while r
eplacing sensitive content with redaction markers that preserve data length information for analysis.

## Features

- **Configurable Redaction**: Define which Kubernetes resource types and fields to sanitize via YAML configuration
- **Parallel Processing**: Multi-threaded file processing for improved performance (defaults to CPU core count)
- **Encrypted Output**: Automatically creates GPG-encrypted tar.gz archives of sanitized data
- **Multiple File Format Support**: Handles YAML, JSON, and other text-based files
- **Path-based Targeting**: Use dot-notation paths to target specific fields within resources
- **Array Support**: Process all elements (`*`) or specific indices in arrays
- **Namespace Filtering**: Optionally limit redaction to specific namespaces

## Installation

Build the tool from source:

```bash
go build -o mco-sanitize .
```

## Usage

### Basic Usage

```bash
# Sanitize a must-gather directory
./mco-sanitize --input /path/to/must-gather

# Sanitize and create encrypted archive
./mco-sanitize --input /path/to/must-gather --output /path/to/sanitized.tar.gz

# Use custom worker count
./mco-sanitize --input /path/to/must-gather --workers 8
```

### Command Line Options

- `--input` (required): Path to the must-gather directory to sanitize
- `--output` (optional): Path where the encrypted tar.gz output should be saved
- `--workers` (optional): Number of worker threads (defaults to CPU core count)

## Configuration

### Default Configuration

The tool includes a built-in default configuration that redacts sensitive fields from common MCO resources:

```yaml
redact:
  - kind: MachineConfig
    apiVersion: machineconfiguration.openshift.io/v1
    paths:
      - spec.config.storage.files.*.contents
      - spec.config.systemd.units.*.contents
      - spec.config.systemd.units.*.dropins.*.contents
  - kind: ControllerConfig
    apiVersion: machineconfiguration.openshift.io/v1
    paths:
      - spec.internalRegistryPullSecret
      - spec.kubeAPIServerServingCAData
      - spec.rootCAData
      - spec.additionalTrustBundle
```

### Custom Configuration

Override the default configuration using the `MCO_MUST_GATHER_SANITIZER_CFG` environment variable:

```bash
# Using a configuration file
export MCO_MUST_GATHER_SANITIZER_CFG="/path/to/config.yaml"

# Using base64-encoded configuration
export MCO_MUST_GATHER_SANITIZER_CFG="cmVkYWN0Og0KIC0ga2luZDogU2VjcmV0..."
```

#### Configuration Format

```yaml
redact:
  - kind: Pod                    # Kubernetes resource kind (required)
    apiVersion: v1               # API version (optional, matches all if empty)
    namespaces:                  # Limit to specific namespaces (optional)
      - kube-system
      - openshift-config
    paths:                       # Fields to redact using dot notation
      - spec.containers.*.env.*.value
      - data.password
      - metadata.annotations.secret-key
```

#### Path Syntax

- Use dot notation to navigate object hierarchies: `spec.containers.0.image`
- Use `*` for all array elements: `spec.containers.*.env.*.value`
- Use numeric indices for specific array elements: `spec.containers.0.ports.1.containerPort`
- Combine object and array navigation: `data.config.yaml.databases.*.password`

## Encryption

### Default Encryption

By default, archives are encrypted using an embedded GPG public key. This ensures that sanitized data remains 
secure during transport and storage.

### Custom Encryption Key

Provide your own GPG public key using the `MCO_MUST_GATHER_SANITIZER_KEY` environment variable:

```bash
# Using base64-encoded public key
export MCO_MUST_GATHER_SANITIZER_KEY="$(base64 -w 0 < /path/to/public-key.asc)"
```

## Redaction Behavior

When a field is redacted, it's replaced with a structured object containing:

```yaml
_REDACTED: "This field has been redacted"
length: 1234  # Original content length in characters
```

This preserves:
- The fact that sensitive data existed
- The approximate size of the original data
- The overall structure of the resource


## Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `MCO_MUST_GATHER_SANITIZER_CFG` | Custom configuration (file path or base64) | `/path/to/config.yaml` |
| `MCO_MUST_GATHER_SANITIZER_KEY` | Custom GPG public key (base64-encoded) | `LS0tLS1CRUdJTi...` |

## Development

### Running Tests

```bash
go test ./...
```

### Adding New Redaction Rules

1. Update the default configuration in `data/default-config.yaml`
2. Add test cases in the `testdata/` directory
3. Run tests to verify behavior