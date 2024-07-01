#!/bin/bash

# Function to check if a version contains setup-envtest
check_version() {
    version=$1
    go mod edit -require=sigs.k8s.io/controller-runtime@${version}
    go mod tidy
    if go list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime/tools/setup-envtest &> /dev/null; then
        echo "${version}"
        return 0
    else
        return 1
    fi
}

# Known good versions to check
versions=("v0.13.0" "v0.12.1" "v0.12.0" "v0.11.0")

for version in "${versions[@]}"; do
    if result=$(check_version $version); then
        echo $result
        exit 0
    fi
done

echo "No suitable version found" >&2
exit 1
