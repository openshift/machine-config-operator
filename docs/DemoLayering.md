Launch a cluster with `OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE=quay.io/mkenigs/origin-release:demo-3-24`
```bash
# set NODE to a worker
oc label node $NODE node-role.kubernetes.io/layered=
oc apply -f ./hack/layering/layered-pool.yaml

# to test adding a file with a MachineConfig
oc apply -f ./hack/layering/file.yaml

# to test live apply
oc apply -f ./hack/layering/live-apply.yaml
```