# get name of node
function node
    set ROLE $argv[1]
    set NUMBER $argv[2]
    oc get nodes --selector node-role.kubernetes.io/"$ROLE"= --output jsonpath="{.items[$NUMBER].metadata.name}"
end

# debug MCD pod
function debug
    set ROLE $argv[1]
    set NUMBER $argv[2]
    oc debug (mcd $ROLE $NUMBER) -n openshift-machine-config-operator
end

# get MCD pod name
function mcd
    set ROLE $argv[1]
    set NUMBER $argv[2]
    oc get pods -n openshift-machine-config-operator --field-selector spec.nodeName=(node $ROLE $NUMBER) --output jsonpath="{.items[0].metadata.name}"
end

# follow MCD logs
function logs
    set ROLE $argv[1]
    set NUMBER $argv[2]
    oc logs -f -n openshift-machine-config-operator -c machine-config-daemon pod/(mcd $ROLE $NUMBER)
end

# exec command on MCD pod
function mcd_exec
    set ROLE $argv[1]
    set NUMBER $argv[2]
    shift 2
    oc exec -n openshift-machine-config-operator -c machine-config-daemon pod/(mcd $ROLE $NUMBER) -- $argv
end