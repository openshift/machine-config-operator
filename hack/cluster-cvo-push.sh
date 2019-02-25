#!/usr/bin/env bash

# Build all images and push to to the cluster registry, then
# build a release image referencing them and start an upgrade to that.
# You must have first run `cluster-push-prep.sh` once.
# Assumptions: You have set KUBECONFIG to point to your local cluster,
# and you have exposed the registry via e.g.
# https://github.com/openshift/installer/issues/411#issuecomment-445165262

set -xeuo pipefail

do_build=1
if [ "${1:-}" = "-n" ]; then
    do_build=0
fi

NS=openshift-machine-config-operator
COMPONENTS="operator controller server daemon"
podman=${podman:-podman}

registry=$(oc get -n openshift-image-registry -o json route/image-registry | jq -r ".spec.host")
curl -k --head https://"${registry}" >/dev/null
registry_ns="${registry}/${NS}"

running_version=$(oc get clusterversion -o=jsonpath='{.items[0].status.desired.image}')

# Keep in sync with build-image.sh
VERSION=$(git describe --tags --abbrev=12 --always)
if [ "${do_build}" = 1 ]; then
    make images
fi
declare -A imgbuilds
for c in $COMPONENTS; do
    imgname="machine-config-${c}:${VERSION}"
    remote_name=${registry_ns}/${imgname}
    $podman push --tls-verify=false localhost/${imgname} ${remote_name}
    digest=$(skopeo inspect --tls-verify=false docker://${remote_name} | jq -r .Digest)
    imgbuilds[$c]="${digest}"
done

dev_img=${registry_ns}/mcodev:latest
oc adm release new --from-release ${running_version} --to-image=${dev_img} --insecure \
   machine-config-operator=${registry_ns}/machine-config-operator:${VERSION} \
   machine-config-controller=${registry_ns}/machine-config-controller:${VERSION} \
   machine-config-server=${registry_ns}/machine-config-server:${VERSION} \
   machine-config-daemon=${registry_ns}/machine-config-daemon:${VERSION}

oc adm upgrade --to-image "${dev_img}" --force
