FROM registry.svc.ci.openshift.org/openshift/release:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN WHAT=machine-config-server ./hack/build-go.sh

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-server /usr/bin/
ENTRYPOINT ["/usr/bin/machine-config-server"]
