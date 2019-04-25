#!/bin/bash
# Test an OS update
set -xeuo pipefail

dn=$(cd $(dirname $0) && pwd)
python=$(for x in python3 python2; do if type $x &>/dev/null; then echo $x; break; fi; done)
tmpd=$(mktemp -d)
cd ${tmpd}

NS=openshift-machine-config-operator

internal_registry="image-registry.openshift-image-registry.svc:5000"
internal_registry_ns="${internal_registry}/${NS}"

running_version=$(oc get clusterversion -o=jsonpath='{.items[0].status.desired.image}')

# Copy the pull secret into our namespace
if ! oc -n "${NS}" get secret/pull-secret >/dev/null; then
  oc -n openshift-config get -o json --export secret/pull-secret | oc -n "${NS}" create -f -
fi
oc -n "${NS}" secrets link builder pull-secret

# Allow unauthenticated pulls so the nodes can fetch
oc -n "${NS}" policy add-role-to-user registry-viewer system:anonymous
oc -n "${NS}" create imagestream mco-os || true
base_machineos_image=$(oc -n openshift-cluster-version rsh deploy/cluster-version-operator cluster-version-operator image machine-os-content)
sed -e "s,MACHINEOSCONTENT,${base_machineos_image}," < ${dn}/mco-os-bc.yaml > mco-os-bc.yaml
oc -n "${NS}" create -f mco-os-bc.yaml || true
if [ -z "$(oc -n ${NS} get -o name builds -l buildconfig=mco-os)" ]; then
    oc -n "${NS}" start-build -F -w mco-os
fi
oc -n "${NS}" get -o json is/mco-os > mco-os.json
${python} > mco-os-ref.txt << EOF
import json
with open("mco-os.json") as f:
  d = json.load(f)
for t in d['status']['tags']:
  if t['tag'] == 'latest':
    print(t['items'][0]['dockerImageReference'])
EOF

updateimg=${internal_registry_ns}/mcodev:latest
# - Update the CA roots with the service CA so we can talk to the registry
# - Copy the cluster pull secret so we can fetch the release image components
# - Add the internal registry creds on top of that
# - Build a custom release image override 
cmd="
cp -p --reflink=auto /run/secrets/kubernetes.io/serviceaccount/*.crt /etc/pki/ca-trust/source/anchors/ && \
update-ca-trust && \
mkdir -p /root/.docker && \
cp -Lp --reflink=auto /etc/pull-secret/.dockerconfigjson /root/.docker/config.json && \
oc registry login && \
oc adm release new --from-release ${running_version} --name=1.0.0 --to-image=${updateimg} machine-os-content=$(cat mco-os-ref.txt)"
cli_image=$(oc -n openshift-cluster-version rsh deploy/cluster-version-operator cluster-version-operator image cli)
buildpod=pod/mco-build-update-image
if ! oc -n "${NS}" get ${buildpod} 2>/dev/null; then
oc -n ${NS} create -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mco-build-update-image
spec:
  serviceAccountName: builder
  containers:
  - name: mco-build-update-image
    image: ${cli_image}
    command: ["/bin/sh", "-c", "${cmd}"]
    volumeMounts:
    - name: pull-secret
      mountPath: "/etc/pull-secret"
      readOnly: true
  volumes:
  - name: pull-secret
    secret:
      secretName: pull-secret  
  restartPolicy: Never
EOF
fi
oc -n "${NS}" wait --for=condition=Ready ${buildpod}
oc -n "${NS}" logs -f ${buildpod} &
timeout 5m /bin/sh -c "while true; do
                         sleep 5
                         phase=\$(oc -n ${NS} get -o json ${buildpod} | jq -r '.status.phase')
                         case \$phase in
                           Succeeded) exit 0;;
                           Failed) exit 1;;
                         esac; done;"
# Get the digest; yeah this is gross
digest=$(oc -n "${NS}" logs --tail=50 ${buildpod} | grep -Ee '^Pushed.*mcodev:latest' | grep -o 'sha256:[^ ]*')
updateimg=${internal_registry_ns}/mcodev@${digest}
oc adm upgrade --to-image "${updateimg}" --force
# Wait for the upgrade to complete
timeout 30m ${dn}/wait-cvo.sh ${NS}
oc get clusterversion
oc get machineconfigpools

oc get nodes -o name | tee nodes.txt
node=$(head -1 nodes.txt)
oc debug ${node} -- chroot /host cat /usr/share/mco-test.txt
echo "OS update test succeeded."
