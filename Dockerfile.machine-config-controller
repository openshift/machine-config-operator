FROM registry.svc.ci.openshift.org/openshift/release:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN WHAT=machine-config-controller ./hack/build-go.sh

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-controller /usr/bin/
COPY templates /etc/mcc/templates
ENTRYPOINT ["/usr/bin/machine-config-controller"]
