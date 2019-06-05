FROM registry.svc.ci.openshift.org/openshift/release:golang-1.12 AS builder
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN WHAT=machine-config-daemon ./hack/build-go.sh

# Temporarily until the UBI base allows us to install util-linux without entitlements
FROM registry.fedoraproject.org/fedora:29
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/_output/linux/amd64/machine-config-daemon /usr/bin/
RUN if ! rpm -q util-linux; then yum install -y util-linux && yum clean all && rm -rf /var/cache/yum/*; fi
ENTRYPOINT ["/usr/bin/machine-config-daemon"]
