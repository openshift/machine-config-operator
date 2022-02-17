#!/usr/bin/env bash

target_mcd_pod="$1"

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

# shellcheck disable=SC1091
source "$SCRIPT_DIR/lib"

copy_binary() {
  local -r pod="$1"
  local -r bin_path="$2"

  echo "Copying to $pod..."
  oc cp -n "$MCO_NAMESPACE" -c "$MCD_CONTAINER_NAME" "$bin_path" "$pod:$ROOTFS_MCD_PATH"
}

push_binary_to_pod() {
  local -r pod="$1"
  local -r bin_path="$2"

  local -r local_bin_sha256sum="$(sha256sum "$bin_path" | awk '{print $1;}')"

  # Check if we have the file on the pod in question
  oc rsh -n "$MCO_NAMESPACE" -c "$MCD_CONTAINER_NAME" "pod/$pod" sha256sum "$ROOTFS_MCD_PATH"
  local -r has_file_retval="$?"

  # We don't have the file, so lets copy it
  if [ "$has_file_retval" -ne 0 ]; then
    echo "Binary not found on $pod"
    copy_binary "$pod" "$bin_path"
    return
  fi

  local -r remote_bin_sha256sum="$(oc rsh -n "$MCO_NAMESPACE" -c "$MCD_CONTAINER_NAME" "pod/$pod" sha256sum "$ROOTFS_MCD_PATH" | awk '{print $1;}')"

  if [[ "$local_bin_sha256sum" == "$remote_bin_sha256sum" ]]; then
    echo "Skipping copy to $pod, binary $ROOTFS_MCD_PATH with equal checksum found: $local_bin_sha256sum"
    return
  fi

  echo "Local: $local_bin_sha256sum, Remote ($pod): $remote_bin_sha256sum"
  copy_binary "$pod" "$bin_path" "$ROOTFS_MCD_PATH"
}

push_binary_to_all_pods() {
  local -r bin_path="$1"

  # Get our target MCD pods
  echo "Getting MCD pods"
  mcd_pods="$(oc get pods -n "$MCO_NAMESPACE" -l 'k8s-app=machine-config-daemon' -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}')"

  # Concurrently copy the built binary to all MCD pods for speed.
  for pod in $mcd_pods; do
    push_binary_to_pod "$pod" "$bin_path" &
  done

  # Wait for all the copy jobs to complete since we don't block for each one.
  wait

  # Restart the MCD pods
  oc rollout restart -n "$MCO_NAMESPACE" "$MCD_DAEMONSET"
}

main() {
  # Heterogeneous clusters are targeted for OCP 4.11, so this will need to change eventually.
  #
  # Ideas:
  # - Get all individual arches and build for each of those concurrently.
  # - Get a list of MCD pods on nodes and get the arch for each of the nodes just before copying.

  # Gets the architecture and operating system of the first node in the list, e.g., amd64\tlinux
  local -r node_info="$(oc get nodes -o go-template='{{(index .items 0).status.nodeInfo.architecture}}{{"\t"}}{{(index .items 0).status.nodeInfo.operatingSystem}}')"
  local -r cluster_arch="$(echo "$node_info" | cut -f1)"

  # Not really needed, but lets not make assumptions :)
  local -r cluster_os="$(echo "$node_info" | cut -f2)"

  echo "Detected cluster arch / OS: $cluster_arch/$cluster_os"

  echo "Building MCD binary..."
  # Need to set both GOOS / GOARCH on Mac otherwise the built binaries will be
  # compiled for a Darwin target instead.
  #
  # Set WHAT to only build the MCD since that's the only component we're
  # interested in at this time.
  GOOS="$cluster_os" GOARCH="$cluster_arch" WHAT=machine-config-daemon "$SCRIPT_DIR/build-go.sh"
  compile_retval="$?"
  if [ $compile_retval -ne 0 ]; then
    echo "Compilation failed!"
    return $compile_retval
  fi

  local -r bin_path="./_output/$cluster_os/$cluster_arch/machine-config-daemon"

  # Get the hash of our MCD binary; useful to compare to the startup value in the MCD pod logs
  sha256sum "$bin_path"

  if [ -z "$target_mcd_pod" ]; then
    # We're not targeting a specific pod, so push the binary to all MCD pods
    echo "Will copy to all MCD pods..."
    push_binary_to_all_pods "$bin_path"
  else
    # We're targeting a specific pod, so only push the binary to that pod
    echo "Pod $target_mcd_pod specified, will only copy to this pod..."
    push_binary_to_pod "$target_mcd_pod" "$bin_path"

    # Delete the pod to force pod re-creation so the new binary will be used.
    oc delete -n "$MCO_NAMESPACE" "pod/$target_mcd_pod"
  fi

  # Wait for the MCD to finish restarting (not strictly required, but doesn't hurt)
  echo "Waiting for MCD daemonsets to restart"
  oc rollout status -w -n "$MCO_NAMESPACE" "$MCD_DAEMONSET"

  echo "Current MCD Pods:"
  "$SCRIPT_DIR/get-mcd-nodes.py"
}

can_run && main
