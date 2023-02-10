#!/usr/bin/env python3

# This is a simple script that grabs all the cluster nodes and associates them
# with a given MCD pod and outputs their roles. There is probably an easier way
# to do this that I am too naïve to be aware of :). Running this script yields
# the following output:
#
# Current MCD Pods:
# machine-config-daemon-9px26     ip-10-0-161-151.ec2.internal    worker
# machine-config-daemon-cpt8q     ip-10-0-130-41.ec2.internal     master
# machine-config-daemon-jnjx4     ip-10-0-137-167.ec2.internal    worker
# machine-config-daemon-klclf     ip-10-0-152-65.ec2.internal     master
# machine-config-daemon-kml9h     ip-10-0-171-232.ec2.internal    master
# machine-config-daemon-t6v5j     ip-10-0-155-187.ec2.internal    worker
#
# Note: This script defaults to using the 'oc' binary. If you are debugging a
# must-gather, this script may be combined with the the 'omc' tool
# (https://github.com/gmeghnag/omc), by passing 'omc' as a CLI flag, like so:
#
# $ ./hack/get-mcd-nodes.py omc

import json
import os
import shutil
import subprocess
import sys

def run_cmd_json(cmd):
    """Runs an arbitrary oc or omc command and returns a dictionary with JSON
    from the output.
    """
    cmd = cmd.split(" ")
    cmd.append("--output=json")
    cmd = subprocess.run(cmd, capture_output=True)
    return json.loads(cmd.stdout)

def get_max_len(in_string, max_len):
    """Gets current length of string and returns it if it exceeds the provided
    max_len. Otherwise, returns the provided max_len.
    """
    curr_len = len(in_string)
    if curr_len > max_len:
        return curr_len
    return max_len

def is_cmd_in_path(cmd):
    """Determines if a given command is found via the $PATH variable."""
    if not shutil.which(cmd):
        print("ERROR: '%s' command missing from your $PATH")
        return False

    return True

def can_run_oc():
    """Determines if one can run the oc binary based upon whether the binary is
    on ones machine and the KUBECONFIG env var is set."""
    kubeconfig = os.environ.get("KUBECONFIG")
    if not kubeconfig:
        print("ERROR: Expected to find $KUBECONFIG")
        return False

    if not os.path.exists(kubeconfig):
        print("ERROR: No kubeconfig found at", kubeconfig)
        return False

    return is_cmd_in_path("oc")

def can_run_omc():
    """Determines if one can run the omc binary based upon whether the binary
    is in ones PATH."""
    return is_cmd_in_path("omc")

def can_run(binary):
    """Determines whether omc or oc can be run."""
    known_binaries = {
        "omc": can_run_omc,
        "oc": can_run_oc,
    }

    validator = known_binaries.get(binary)
    if not validator:
        print("ERROR: unknown command '%s'", binary)
        return False

    return validator()

def main(binary="oc"):
    if not can_run(binary):
        sys.exit(1)

    # Get all the MCD pods
    mcd_pods = run_cmd_json("%s get pods -n openshift-machine-config-operator -l k8s-app=machine-config-daemon" % binary)

    # Get our nodes and group by node name
    nodes_by_name = {node["metadata"]["name"]: node
                    for node in run_cmd_json("%s get nodes" % binary)["items"]}

    out = []
    node_name_max_len = 0
    pod_name_max_len = 0

    for pod in mcd_pods["items"]:
        pod_name = pod["metadata"]["name"]
        node_name = pod["spec"]["nodeName"]
        # Get the node the MCD pod is running on
        node = nodes_by_name[node_name]

        # Get max pod name length; used to format output
        pod_name_max_len = get_max_len(pod_name, pod_name_max_len)

        # Get max node name length; used to format output
        node_name_max_len = get_max_len(node_name, node_name_max_len)

        # Lazily get node roles
        roles = (label.split("/")[1]
                for label in node["metadata"]["labels"].keys()
                if "node-role.kubernetes.io" in label)

        # Insert our results into an output list
        out.append((pod_name, node_name, ','.join(roles)))

    # Output format template
    tmpl = "{: <%s}\t{: <%s}\t{: <6}" % (pod_name_max_len, node_name_max_len)

    # Print our output
    for item in out:
        print(tmpl.format(*item))

if __name__ == "__main__":
    if len(sys.argv) > 1:
        main(sys.argv[1])
    else:
        main()
