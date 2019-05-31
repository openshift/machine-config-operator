FROM registry.svc.ci.openshift.org/ocp/builder:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN WHAT=machine-config-daemon ./hack/build-go.sh; \
    mkdir -p /tmp/build; \
    cp /go/src/github.com/openshift/machine-config-operator/_output/linux/$(go env GOARCH)/machine-config-daemon /tmp/build/machine-config-daemon

FROM registry.svc.ci.openshift.org/ocp/4.0:base
COPY --from=builder /tmp/build/machine-config-daemon /usr/bin/
RUN yum install -y util-linux && yum clean all && rm -rf /var/cache/yum/*
ENTRYPOINT ["/usr/bin/machine-config-daemon"]
