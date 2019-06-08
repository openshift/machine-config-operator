#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT=$(dirname "${BASH_SOURCE}")/..

count=$(diff -U 0 "${REPO_ROOT}/templates/worker/01-worker-kubelet/openstack/units/kubelet.yaml" "${REPO_ROOT}/templates/worker/01-worker-kubelet/_base/units/kubelet.yaml" | grep -v ^@ | grep -v '^---' | grep -v '^+++' | wc -l || /bin/true)
if [[ $count -ne 1 ]]; then
	echo "Too many differences in the worker template"
	exit 1
fi
count=$(diff -U 0 "${REPO_ROOT}/templates/master/01-master-kubelet/openstack/units/kubelet.yaml" "${REPO_ROOT}/templates/master/01-master-kubelet/_base/units/kubelet.yaml" | grep -v ^@ | grep -v '^---' | grep -v '^+++' | wc -l || /bin/true)
if [[ $count -ne 1 ]]; then
	echo "Too many differences in the master template"
	exit 1
fi
