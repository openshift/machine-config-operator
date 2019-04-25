#!/bin/bash
set -euo pipefail
NS=$1
shift
while true; do
  sleep 5
  if ! oc -n ${NS} get -o json clusterversion > /tmp/clusterversion.json; then
     continue
  fi
  failing=$(jq -r '.items[0].status.conditions | map(select(.type == "Failing"))[0] | .status' < /tmp/clusterversion.json)
  if [ "${failing}" = "True" ]; then
    echo failing
    jq . < /tmp/clusterversion.json
    exit 1
  fi
  progressing=$(jq -r '.items[0].status.conditions | map(select(.type == "Progressing"))[0] | .status' < /tmp/clusterversion.json)
  if [ "${progressing}" = "False" ]; then
    break
  fi
done
echo "Successfully waited for clusterversion"