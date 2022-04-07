#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

# shellcheck disable=SC1091
source "$SCRIPT_DIR/lib"

copy_mcd_to_disk() {
  pod="$1"

  oc debug \
    -n "$MCO_NAMESPACE" \
    -c "$MCD_CONTAINER_NAME" \
    --one-container=true \
    "pod/$pod" -- cp -v /usr/bin/machine-config-daemon "$ROOTFS_MCD_PATH"
}

main() {
  # Disable the cluster version operator
  # --replicas 1 out-of-the-box
  oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator

  # Disable the MCO
  # --replicas 1 out-of-the-box
  oc scale --replicas 0 -n "$MCO_NAMESPACE" deployments/machine-config-operator

  # Copy the MCD from the container to rootfs first to avoid startup issues
  echo "Copy MCD from container to /rootfs"
  mcd_pods="$(oc get pods -n "$MCO_NAMESPACE" -l 'k8s-app=machine-config-daemon' -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}')"
  for pod in $mcd_pods; do
    copy_mcd_to_disk "$pod" &
  done

  echo "Waiting for copies to finish..."
  wait
  echo "Done"

  # Now we modify the MCD daemonset to copy the MCD binary from rootfs and run it
  # Note: This was originally done with a read, however read exits with code 1.
  patch="$(/bin/cat <<- EOM
spec:
  template:
    spec:
      containers:
      - args:
          - -c
          - "sha256sum $ROOTFS_MCD_PATH && cp $ROOTFS_MCD_PATH /usr/local/bin/machine-config-daemon && /usr/local/bin/machine-config-daemon start -v 4"
        command: ["/bin/bash"]
        name: machine-config-daemon
EOM
)"

  echo "Modifying the MCD daemonset"

  # Apply the modifications
  oc patch \
    daemonset/machine-config-daemon \
    -n "$MCO_NAMESPACE" \
    --patch="$patch"

  # Wait for the updated daemonset to roll out
  oc rollout status -n "$MCO_NAMESPACE" -w "$MCD_DAEMONSET"
}

can_run && main
