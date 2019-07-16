#!/usr/bin/env bash

# Build an image and push it to the cluster registry.
# You must have first run `cluster-push-prep.sh` once.
# Assumptions: You have set KUBECONFIG to point to your local cluster,
# and you have exposed the registry via e.g.
# https://github.com/openshift/installer/issues/411#issuecomment-445165262

set -xeuo pipefail

podman=${podman:-podman}

do_build=1
if [ "${1:-}" = "-n" ]; then
    do_build=0
fi

registry=$(oc get -n openshift-image-registry -o json route/image-registry | jq -r ".spec.host")
curl -k --head https://"${registry}" >/dev/null

imgname=machine-config-operator
LOCAL_IMGNAME=localhost/${imgname}:latest
REMOTE_IMGNAME=openshift-machine-config-operator/${imgname}
if [ "${do_build}" = 1 ]; then
    ./hack/build-image.sh
fi
$podman push --tls-verify=false "${LOCAL_IMGNAME}" ${registry}/${REMOTE_IMGNAME}

digest=$(skopeo inspect --tls-verify=false docker://${registry}/${REMOTE_IMGNAME} | jq -r .Digest)
imageid=${REMOTE_IMGNAME}@${digest}

oc project openshift-machine-config-operator

IN_CLUSTER_NAME=image-registry.openshift-image-registry.svc:5000/${imageid}

# Scale down the operator now to avoid it racing with our update.
oc scale --replicas=0 deploy/machine-config-operator

# Patch the images.json
tmpf=$(mktemp)
oc get -o json configmap/machine-config-operator-images > ${tmpf}
outf=$(mktemp)
python3 > ${outf} <<EOF
import sys,json
cm=json.load(open("${tmpf}"))
images = json.loads(cm['data']['images.json'])
for k in images:
  if k.startswith('machineConfig'):
    images[k] = "${IN_CLUSTER_NAME}"
cm['data']['images.json'] = json.dumps(images)
json.dump(cm, sys.stdout)
EOF
oc replace -f ${outf}
rm ${tmpf} ${outf}

for x in operator controller server daemon; do
patch=$(mktemp)
cat >${patch} <<EOF
spec:
  template:
     spec:
       containers:
         - name: machine-config-${x}
           image: ${IN_CLUSTER_NAME}
EOF

# And for speed, patch the deployment directly rather
# than waiting for the operator to start up and do leader
# election.
case $x in
    controller|operator)
        target=deploy/machine-config-${x}
        ;;
    daemon|server)
        target=daemonset/machine-config-${x}
        ;;
    *) echo "Unhandled $x" && exit 1
esac

oc patch "${target}" -p "$(cat ${patch})"
rm ${patch}
echo "Patched ${target}"
done
oc scale --replicas=1 deploy/machine-config-operator
