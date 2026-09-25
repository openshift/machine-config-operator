FROM quay.io/hummingbird/nodejs:25
ENV NPM_CONFIG_PREFIX=/tmp/npm-global
ENV PATH=$NPM_CONFIG_PREFIX/bin:$PATH
WORKDIR /workdir
COPY install-markdownlint.sh /tmp
RUN /tmp/install-markdownlint.sh
ENTRYPOINT /workdir/hack/markdown/markdownlint.sh
