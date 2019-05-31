FROM registry.svc.ci.openshift.org/ocp/builder:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN WHAT=setup-etcd-environment ./hack/build-go.sh; \
    mkdir -p /tmp/build; \
    cp /go/src/github.com/openshift/machine-config-operator/_output/linux/$(go env GOARCH)/setup-etcd-environment /tmp/build/setup-etcd-environment

FROM registry.svc.ci.openshift.org/ocp/4.0:base
COPY --from=builder /tmp/build/setup-etcd-environment /usr/bin/
ENTRYPOINT ["/usr/bin/setup-etcd-environment"]
