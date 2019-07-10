# This is the "all in one" image for the MCO; we previously
# had multiple separate images.  See https://github.com/openshift/machine-config-operator/issues/739
FROM registry.svc.ci.openshift.org/openshift/release:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN make binaries

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
# Needed by the MCD to run `mount` before chrooting
RUN if ! rpm -q util-linux; then yum install -y util-linux && yum clean all && rm -rf /var/cache/yum/*; fi
# Install our binaries
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/*/* /usr/bin/
# These are used by the MCO
COPY install /manifests
# These are used by the MCC
COPY templates /etc/mcc/templates
ENTRYPOINT ["/usr/bin/machine-config-operator"]
LABEL io.openshift.release.operator true
