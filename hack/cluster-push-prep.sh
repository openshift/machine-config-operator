#!/usr/bin/env bash

# Scale the CVO down and set up podman with a secret ready to push
# to the machine-config-operator namespace.

# Assumptions: You have set KUBECONFIG to point to your local cluster,
# and you have exposed the registry via e.g.
# https://github.com/openshift/installer/issues/411#issuecomment-445165262

set -xeuo pipefail

# XXX: --type merge completely overrides any previous "overrides" array
#      find a way to just append? json op: add isn't working at all
#      if there's not an overrides array already, that's why we use merge
oc patch clusterversions.config.openshift.io/version --type merge -p '{"spec":{"overrides": [{"kind": "Deployment","name": "machine-config-operator", "namespace": "openshift-machine-config-operator", "unmanaged": true}, {"kind": "ConfigMap","name": "machine-config-operator-images", "namespace": "openshift-machine-config-operator", "unmanaged": true}]}}'

if ! oc get -n openshift-image-registry route/image-registry &>/dev/null; then
    oc expose -n openshift-image-registry svc/image-registry
fi
oc patch -n openshift-image-registry route/image-registry -p '{"spec": {"tls": {"insecureEdgeTerminationPolicy": "Redirect", "termination": "reencrypt"}}}'
registry=$(oc get -n openshift-image-registry -o json route/image-registry | jq -r ".spec.host")
if ! curl -k -s --head https://"${registry}" >/dev/null; then
    if ! grep -q "${registry}" /etc/hosts; then
        set +x
        echo "error: Failed to contact the registry"
        echo "The problem may be DNS; you can e.g. add the registry to your /etc/hosts - as root run:"
        echo "  echo 127.0.0.1 ${registry} >> /etc/hosts"
        exit 1
    fi
fi
builder_secretid=$(oc get -n openshift-machine-config-operator secret | egrep '^builder-token-'| head -1 | cut -f 1 -d ' ')
echo "podman login ${registry} ..."
set +x
secret="$(oc get -n openshift-machine-config-operator -o json secret/${builder_secretid} | jq -r '.data.token' | base64 -d)"
podman login --tls-verify=false -u unused -p "${secret}" "${registry}"
set -x

# And allow everything to pull from our namespace
oc -n openshift-machine-config-operator policy add-role-to-group registry-viewer system:unauthenticated
