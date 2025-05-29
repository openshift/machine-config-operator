#!/bin/bash
# MCO diagnostic script

echo "Collecting MCO diagnostic information..."

# Set sensitive environment variables
export DB_PASSWORD="super-secret-password"
export API_TOKEN="bearer-token-12345"
export REGISTRY_SECRET="docker-registry-secret"

# Collect node information
kubectl get nodes -o yaml > nodes.yaml
kubectl get machineconfigs -o yaml > machineconfigs.yaml

# Collect logs with sensitive data
oc logs -n openshift-machine-config-operator deployment/machine-config-controller > mco-controller.log

echo "Diagnostic collection complete"
echo "Files may contain sensitive information - please sanitize before sharing"