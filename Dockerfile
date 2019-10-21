FROM registry.svc.ci.openshift.org/openshift/release:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN make binaries

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
# FIXME once we can depend on build hosts having new enough container runtime
# to support globs, let's use that.
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-operator /usr/bin/
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-daemon /usr/bin/
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-controller /usr/bin/
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-server /usr/bin/
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/setup-etcd-environment /usr/bin/
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/gcp-routes-controller /usr/bin/
COPY install /manifests
RUN if ! rpm -q util-linux; then yum install -y util-linux && yum clean all && rm -rf /var/cache/yum/*; fi
COPY templates /etc/mcc/templates
ENTRYPOINT ["/usr/bin/machine-config-operator"]
LABEL io.openshift.release.operator true
