# Use RHEL 9 as the primary builder base for the Machine Config Operator
FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.24-openshift-4.22 AS rhel9-builder
ARG TAGS=""
WORKDIR /go/src/github.com/openshift/machine-config-operator
COPY . .
RUN make install DESTDIR=./instroot-rhel9

# Add a RHEL 8 builder to compile the RHEL 8 compatible binaries
FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.24-openshift-4.22 AS rhel8-builder
ARG TAGS=""
WORKDIR /go/src/github.com/openshift/machine-config-operator
# Copy the RHEL 8 machine-config-daemon binary and rename
COPY . .
RUN make install DESTDIR=./instroot-rhel8

FROM registry.ci.openshift.org/ocp/4.22:base-rhel9
ARG TAGS=""
COPY install /manifests
RUN if [ "${TAGS}" = "fcos" ]; then \
    # comment out non-base/extensions image-references entirely for fcos
    sed -i '/- name: rhel-coreos-/,+3 s/^/#/' /manifests/image-references && \
    # comment out rhel-coreos-10 entirely for fcos
    sed -i '/- name: rhel-coreos-10/,+3 s/^/#/' /manifests/image-references && \
    # also remove extensions from the osimageurl configmap (if we don't, oc won't rewrite it, and the placeholder value will survive and get used)
    sed -i '/baseOSExtensionsContainerImage:/ s/^/#/' /manifests/0000_80_machine-config_05_osimageurl.yaml && \
    # rewrite image names for fcos
    sed -i 's/rhel-coreos/fedora-coreos/g' /manifests/*; \
    elif [ "${TAGS}" = "scos" ]; then \
    # comment out rhel-coreos-10 for scos
    sed -i '/- name: rhel-coreos-10/,+3 s/^/#/' /manifests/* && \
    # rewrite image names for scos
    sed -i 's/rhel-coreos/stream-coreos/g' /manifests/*; fi && \
    dnf -y install 'nmstate >= 2.2.10' && \
    if ! rpm -q util-linux; then dnf install -y util-linux; fi && \
    # We also need to install fuse-overlayfs and cpp for Buildah to work correctly.
    if ! rpm -q buildah; then dnf install -y buildah fuse-overlayfs cpp --exclude container-selinux; fi && \
    # Create the build user which will be used for doing OS image builds. We
    # use the username "build" and the uid 1000 since this matches what is in
    # the official Buildah image.
    # Conditional checks if "build" user does not exist before adding user.
    if ! id -u "build" >/dev/null 2>&1; then useradd --uid 1000 build; fi && \
    dnf clean all && \
    rm -rf /var/cache/dnf/*
# Copy the binaries directly from their build stages into their final location.
# Do this after package installation to avoid invalidating state for faster
# local builds.
COPY --from=rhel9-builder /go/src/github.com/openshift/machine-config-operator/instroot-rhel9/usr/bin/* /usr/bin/
# Copy the RHEL 8 machine-config-daemon binary and rename
COPY --from=rhel8-builder /go/src/github.com/openshift/machine-config-operator/instroot-rhel8/usr/bin/machine-config-daemon /usr/bin/machine-config-daemon.rhel8
COPY templates /etc/mcc/templates
ENTRYPOINT ["/usr/bin/machine-config-operator"]
LABEL io.openshift.release.operator true
