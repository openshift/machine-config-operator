#!/usr/bin/env bash

# Build an image and push it to the cluster registry.
# You must have first run `cluster-push-prep.sh` once.
# Assumptions: You have set KUBECONFIG to point to your local cluster,
# and you have exposed the registry via e.g.
# https://github.com/openshift/installer/issues/411#issuecomment-445165262

set -xeuo pipefail

do_build=1
if [ "${1:-}" = "-n" ]; then
    do_build=0
fi

registry=$(oc get -n openshift-image-registry -o json route/image-registry | jq -r ".spec.host")
curl -k --head https://"${registry}" >/dev/null

WHAT=${WHAT:-machine-config-daemon}
LOCAL_IMGNAME=localhost/${WHAT}
REMOTE_IMGNAME=openshift-machine-config-operator/${WHAT}
if [ "${do_build}" = 1 ]; then
    podman build -t "${LOCAL_IMGNAME}" -f Dockerfile.${WHAT} --no-cache
    podman push --tls-verify=false "${LOCAL_IMGNAME}" ${registry}/${REMOTE_IMGNAME}
fi

digest=$(skopeo inspect --tls-verify=false docker://${registry}/${REMOTE_IMGNAME} | jq -r .Digest)
imageid=${REMOTE_IMGNAME}@${digest}

oc project openshift-machine-config-operator

IN_CLUSTER_NAME=image-registry.openshift-image-registry.svc:5000/${imageid}

# Patch the images.json
tmpf=$(mktemp)
oc get -o json configmap/machine-config-operator-images > ${tmpf}
outf=$(mktemp)
python3 > ${outf} <<EOF
import sys,json
what="${WHAT}".split('-')[-1]
cm=json.load(open("${tmpf}"))
images = json.loads(cm['data']['images.json'])
images["machineConfig"+what[0].upper()+what[1:]] = "${IN_CLUSTER_NAME}"
cm['data']['images.json'] = json.dumps(images)
json.dump(cm, sys.stdout)
EOF
oc replace -f ${outf}
rm ${tmpf} ${outf}

# The operator still controls the deployment, scale it down
# and patch directly for increased speed
oc scale --replicas=0 deploy/machine-config-operator
patch=$(mktemp)
cat >${patch} <<EOF
spec:
  template:
     spec:
       containers:
         - name: ${WHAT}
           image: ${IN_CLUSTER_NAME}
EOF

# And for speed, patch the deployment directly
case $WHAT in
    machine-config-controller|machine-config-operator)
        target=deploy/${WHAT}
        ;;
    machine-config-daemon|machine-config-server)
        target=daemonset/${WHAT}
        ;;
    *) echo "Unhandled WHAT=$WHAT" && exit 1
esac

oc patch "${target}" -p "$(cat ${patch})"
rm ${patch}
oc scale --replicas=1 deploy/machine-config-operator
