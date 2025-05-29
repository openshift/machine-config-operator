# E2E Test Data for MCO Sanitizer

This directory contains input files, configuration files, and expected output files (golden files) for end-to-end testing of the MCO sanitizer.

## Directory Structure

```
testdata/
├── input/                    # Original files to be sanitized
│   ├── machineconfig.yaml   # Sample MachineConfig with sensitive data
│   ├── deployment.yaml      # Sample Deployment with arrays
│   ├── secret.json          # Sample Secret in JSON format
│   └── ...
├── configs/                  # Custom configuration files
│   ├── array-config.yaml    # Config for testing array traversal
│   ├── json-config.yaml     # Config for JSON file processing
│   └── ...
└── expected/                 # Golden files (expected outputs after sanitization)
    ├── default-config/       # Expected outputs using default config
    │   └── machineconfig.yaml
    ├── array-traversal/      # Expected outputs for array traversal tests
    │   └── deployment.yaml
    ├── json-files/           # Expected outputs for JSON tests
    │   └── secret.json
    ├── mixed-file-types/     # Expected outputs for mixed file type tests
    │   ├── machineconfig.yaml  # YAML file gets redacted
    │   ├── application.log     # Log file remains unchanged
    │   ├── backup.tar.gz       # Binary files remain unchanged
    │   ├── readme.txt          # Text files remain unchanged
    │   ├── script.sh           # Script files remain unchanged
    │   └── binary-file.bin     # Binary files remain unchanged
    └── ...
```

## How to Add New Test Cases

1. **Add input files** in `input/` directory with realistic Kubernetes manifests
2. **Create config files** in `configs/` directory if using custom redaction rules
3. **Generate golden files** in appropriate `expected/` subdirectories showing expected output
4. **Add test case** to `TestSanitize_E2E_GoldenFiles` in the test file

## Updating Golden Files

When redaction behavior changes and you need to update expected outputs:

```bash
UPDATE_GOLDEN=true go test -run TestSanitize_E2E_GoldenFiles
```