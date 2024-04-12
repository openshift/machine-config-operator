# Use RHEL 9 as the primary builder base for the Machine Config Operator
FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.21-openshift-4.16 AS builder
ARG TAGS=""
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
# FIXME once we can depend on a new enough host that supports globs for COPY,
# just use that.  For now we work around this by copying a tarball.
RUN make install DESTDIR=./instroot && tar -C instroot -cf instroot.tar .

# Add a RHEL 8 builder to compile the RHEL 8 compatible binaries
FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.21-openshift-4.16 AS rhel8-builder
ARG TAGS=""
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN make install DESTDIR=./instroot-rhel8 && tar -C instroot-rhel8 -cf instroot-rhel8.tar .

# Base image is RHEL 9. Only machine-config-daemon.rhel8 is from RHEL 8; all other binaries are RHEL 9.
FROM registry.ci.openshift.org/ocp/4.16:base-rhel9
ARG TAGS=""
COPY --from=builder /go/src/github.com/openshift/machine-config-operator/instroot.tar /tmp/instroot.tar
RUN cd / && tar xf /tmp/instroot.tar && rm -f /tmp/instroot.tar
# Copy the RHEL 8 machine-config-daemon binary and rename
COPY --from=rhel8-builder /go/src/github.com/openshift/machine-config-operator/instroot-rhel8/usr/bin/machine-config-daemon /usr/bin/machine-config-daemon.rhel8
COPY install /manifests

RUN if [ "${TAGS}" = "fcos" ]; then \
    # comment out non-base/extensions image-references entirely for fcos
    sed -i '/- name: rhel-coreos-/,+3 s/^/#/' /manifests/image-references && \
    # also remove extensions from the osimageurl configmap (if we don't, oc won't rewrite it, and the placeholder value will survive and get used)
    sed -i '/baseOSExtensionsContainerImage:/ s/^/#/' /manifests/0000_80_machine-config_05_osimageurl.yaml && \
    # rewrite image names for fcos
    sed -i 's/rhel-coreos/fedora-coreos/g' /manifests/*; \
    elif [ "${TAGS}" = "scos" ]; then \
    # rewrite image names for scos
    sed -i 's/rhel-coreos/centos-stream-coreos-9/g' /manifests/*; fi && \
    dnf -y install 'nmstate >= 2.2.10' && \
    if ! rpm -q util-linux; then dnf install -y util-linux; fi && \
    dnf clean all && rm -rf /var/cache/dnf/*
COPY templates /etc/mcc/templates
ENTRYPOINT ["/usr/bin/machine-config-operator"]
LABEL io.openshift.release.operator true