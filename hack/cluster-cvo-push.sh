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

internal_registry="image-registry.openshift-image-registry.svc:5000"
internal_registry_ns="${internal_registry}/${NS}"
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

updateimg=${internal_registry_ns}/mcodev:latest
cmd="oc adm release new --from-release ${running_version} --to-image=${updateimg} \
   machine-config-operator=${internal_registry_ns}/machine-config-operator:${VERSION} \
   machine-config-controller=${internal_registry_ns}/machine-config-controller:${VERSION} \
   machine-config-server=${internal_registry_ns}/machine-config-server:${VERSION} \
   machine-config-daemon=${internal_registry_ns}/machine-config-daemon:${VERSION} && \
   oc adm upgrade --to-image ${updateimg} --force"
cli_image=$(oc -n openshift-cluster-version rsh deploy/cluster-version-operator cluster-version-operator image cli)
oc -n openshift-cluster-version create -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: mco-build-update-image
spec:
  template:
    spec:
      containers:
      - name: mco-build-update-image
        image: ${cli_image}
        command: ["/bin/sh", "-c", "${cmd}"]
      restartPolicy: Never
EOF



