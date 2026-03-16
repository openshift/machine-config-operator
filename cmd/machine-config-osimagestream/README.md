# machine-config-osimagestream

A CLI tool for retrieving OSImageStream information from OpenShift/OKD release payload images or ImageStream files.

## Overview

The `machine-config-osimagestream` tool is part of the Machine Config Operator and provides utilities for extracting OS image information from release payloads. It supports three main commands:

1. **get osimagestream** - Retrieve the full OSImageStream resource
2. **get default-node-image** - Get the default node OS image pullspec
3. **get os-image-pullspec** - Get the OS image pullspec for a specific stream name

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

### Get OS Image Pullspec by Name

Retrieve the OS image pullspec for a specific OSImageStream name:

```bash
# Get OS image pullspec for rhel-9 stream from release image
machine-config-osimagestream get os-image-pullspec \
  --authfile /path/to/pull-secret.json \
  --release-image quay.io/openshift-release-dev/ocp-release:latest \
  --osimagestream-name rhel-9

# Get OS image pullspec for rhel-10 stream from ImageStream file
machine-config-osimagestream get os-image-pullspec \
  --authfile /path/to/pull-secret.json \
  --imagestream /path/to/imagestream.json \
  --osimagestream-name rhel-10
```

## Environment Variables

The tool respects standard HTTP proxy environment variables:

- `HTTP_PROXY` - HTTP proxy URL
- `HTTPS_PROXY` - HTTPS proxy URL
- `NO_PROXY` - Not currently supported (tracked in MCO-2016)
