This folder contains e2e tests for the MCO Operator.

TODO: Add an overview.

# Running MCO Disruptive Tests
Some of the strangest issues are discovered by running MCO disruptive tests. These are long-running tests that do not run automatically on every PR. Currently, some tests reside in origin and some in this repository, though the plan is to consolidate them all within the MCO repo.

You can run these tests using the following methods:

## Periodic Job
Run the `periodic-ci-openshift-machine-config-operator-release-4.21-periodics-e2e-aws-mco-disruptive-techpreview-1of2` job on any PR.

Standard run: `/payload-job periodic-ci-openshift-machine-config-operator-release-4.21-periodics-e2e-aws-mco-disruptive-techpreview-1of2`

Run with a code change: `/payload-job-with-prs periodic-ci-openshift-machine-config-operator-release-4.21-periodics-e2e-aws-mco-disruptive-techpreview-1of2 openshift/origin#30644`

## On Your Own Cluster
1. Create a cluster and export your `KUBECONFIG` (Tested on an Ubuntu VM).

2. Download the correct version of the `oc` client and update your `$PATH` to include it.

3. Clone the `openshift/origin` repository and build it with your changes.

4. Run the tests:

```
./openshift-tests run openshift/machine-config-operator/disruptive \
  --shard-count 2 \
  --shard-id 1 \
  --provider '{"type":"aws","region":"us-west-2","zone":"us-west-2d","multizone":true,"multimaster":true}' \
  -o /tmp/logs/artifacts/e2e.log \
  --junit-dir /tmp/logs/artifacts/junit
```
