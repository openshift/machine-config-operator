FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.18-openshift-4.12 AS builder
ARG TAGS=""
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
# FIXME once we can depend on a new enough host that supports globs for COPY,
# just use that.  For now we work around this by copying a tarball.
RUN make install DESTDIR=./instroot && tar -C instroot -cf instroot.tar .

FROM registry.ci.openshift.org/ocp/4.12:base
ARG TAGS=""
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/instroot.tar /tmp/instroot.tar
RUN cd / && tar xf /tmp/instroot.tar && rm -f /tmp/instroot.tar
COPY install /manifests

RUN if [[ "${TAGS}" == "fcos" ]] || [[ "${TAGS}" == "scos" ]]; then \
    # comment out non-base/extensions image-references entirely for fcos/scos
    sed -i '/- name: rhel-coreos-8-/,+3 s/^/#/' /manifests/image-references && \
    # also remove extensions from the osimageurl configmap (if we don't, oc won't rewrite it, and the placeholder value will survive and get used)
    sed -i '/baseOSExtensionsContainerImage:/ s/^/#/' /manifests/0000_80_machine-config-operator_05_osimageurl.yaml; fi && \
    # rewrite image names for fcos/scos
    if [[ "${TAGS}" == "fcos" ]]; then sed -i 's/rhel-coreos-8/fedora-coreos/g' /manifests/*; \
    elif [[ "${TAGS}" == "scos" ]]; then sed -i 's/rhel-coreos-8/centos-stream-coreos-9/g' /manifests/*; fi && \
    if ! rpm -q util-linux; then yum install -y util-linux && yum clean all && rm -rf /var/cache/yum/*; fi
COPY templates /etc/mcc/templates
ENTRYPOINT ["/usr/bin/machine-config-operator"]
LABEL io.openshift.release.operator true
