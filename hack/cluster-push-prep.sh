#!/usr/bin/env bash

# Scale the CVO down and set up podman with a secret ready to push
# to the machine-config-operator namespace.

# Assumptions: You have set KUBECONFIG to point to your local cluster,
# and you have exposed the registry via e.g.
# https://github.com/openshift/installer/issues/411#issuecomment-445165262

set -xeuo pipefail

oc -n openshift-cluster-version scale --replicas=0 deploy/cluster-version-operator
if ! oc get -n openshift-image-registry route/image-registry &>/dev/null; then
    oc expose -n openshift-image-registry svc/image-registry
fi
oc patch -n openshift-image-registry route/image-registry -p '{"spec": {"tls": {"insecureEdgeTerminationPolicy": "Redirect", "termination": "reencrypt"}}}'
registry=$(oc get -n openshift-image-registry -o json route/image-registry | jq -r ".spec.host")
if ! curl -k --head https://"${registry}" >/dev/null; then
    if ! grep -q "${registry}" /etc/hosts; then
        set +x
        echo "error: Failed to contact the registry"
        echo "The problem may be DNS; you can e.g. add the registry to your /etc/hosts - as root run:"
        echo "  echo 127.0.0.1 ${registry} >> /etc/hosts"
        exit 1
    fi
fi

# And allow everything to pull from our namespace
oc -n openshift-machine-config-operator policy add-role-to-group registry-viewer system:anonymous
