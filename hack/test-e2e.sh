#!/usr/bin/env bash
#
# This is the "e2e-aws-operator" on github; see github.com/openshift/release
# which contains the Prow glue to invoke it.  Add in tests here specific
# to the machine-config-operator.
#
# Currently, all tests here are non-destructive.  They assume the environment
# has a KUBECONFIG with administrative privileges.

set -xeuo pipefail

# Base functions taken from github.com/ostreedev/ostree/tests/libtest-core.sh;
# if we add more than this let's just copy it wholesale.

fatal() {
    echo $@ 1>&2; exit 1
}
# Dump ls -al + file contents to stderr, then fatal()
_fatal_print_file() {
    file="$1"
    shift
    ls -al "$file" >&2
    sed -e 's/^/# /' < "$file" >&2
    fatal "$@"
}
assert_file_has_content () {
    fpath=$1
    shift
    for re in "$@"; do
        if ! grep -q -e "$re" "$fpath"; then
            _fatal_print_file "$fpath" "File '$fpath' doesn't match regexp '$re'"
        fi
    done
}
assert_file_has_content_literal () {
    fpath=$1; shift
    for s in "$@"; do
        if ! grep -q -F -e "$s" "$fpath"; then
            _fatal_print_file "$fpath" "File '$fpath' doesn't match fixed string list '$s'"
        fi
    done
}

oc project openshift-machine-config-operator

# https://github.com/openshift/machine-config-operator/pull/288/commits/44d5c5215b5450fca32806f796b50a3372daddc2
oc get '--output=jsonpath={.spec.template.spec.nodeSelector}' ds/machine-config-daemon > selector.txt
assert_file_has_content selector.txt "beta.kubernetes.io/os:linux"
rm -f selector.txt
echo "ok daemon nodeSelector"
