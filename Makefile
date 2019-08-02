COMPONENTS = daemon controller server operator

# vim: noexpandtab ts=8
export GOPATH=$(shell echo $${GOPATH:-$$HOME/go})

.PHONY: clean test test-unit test-e2e verify update install-tools
# Remove build artifaces
# Example:
#    make clean
#
clean:
	@rm -rf _output

# Build machine configs. Intended to be called via another target.
# Example:
#    make _build-setup-etcd
_build-%:
	WHAT=$* hack/build-go.sh

# Use podman to build the image.
image:
	hack/build-image

# Build + push + deploy image for a component. Intended to be called via another target.
# Example:
#    make _deploy-machine-config-daemon
_deploy-%:
	WHAT=$* hack/cluster-push.sh

# Run tests
test: test-unit test-e2e

# Unit tests only (no active cluster required)
test-unit:
	go test -count=1 -v ./cmd/... ./pkg/... ./lib/...

# Install build and test dependencies
CRD_SCHEMA_GEN_VERSION := v1.0.0
install-crd-schema-gen:
	git clone -b $(CRD_SCHEMA_GEN_VERSION) --single-branch --depth 1 https://github.com/openshift/crd-schema-gen.git $(CRD_SCHEMA_GEN_GOPATH)/src/github.com/openshift/crd-schema-gen
	GOPATH=$(CRD_SCHEMA_GEN_GOPATH) GOBIN=$(CRD_SCHEMA_GEN_GOPATH)/bin go install $(CRD_SCHEMA_GEN_GOPATH)/src/github.com/openshift/crd-schema-gen/cmd/crd-schema-gen

install-go-bindata:
	hack/install-go-bindata.sh

install-tools: install-go-bindata 
	# mktemp -d is required to avoid the creation of go modules related files in the project root
	cd $(shell mktemp -d) && GO111MODULE=on go get github.com/securego/gosec/cmd/gosec@4b59c948083cd711b6a8aac8f32721b164899f57
	cd $(shell mktemp -d) && GO111MODULE=on go get github.com/golangci/golangci-lint/cmd/golangci-lint@v1.17.1

# Run the code generation tasks.
update-codegen:
	hack/update-codegen.sh

update-crds: CRD_SCHEMA_GEN_GOPATH :=$(shell mktemp -d)
update-crds: install-crd-schema-gen
	$(CRD_SCHEMA_GEN_GOPATH)/bin/crd-schema-gen --apis-dir pkg/apis/machineconfiguration.openshift.io/v1 --manifests-dir manifests --output-dir manifests

update: install-tools update-codegen update-crds
	hack/update-generated-bindata.sh

verify-crds: CRD_SCHEMA_GEN_GOPATH :=$(shell mktemp -d)
verify-crds: install-crd-schema-gen
	$(CRD_SCHEMA_GEN_GOPATH)/bin/crd-schema-gen --apis-dir pkg/apis/machineconfiguration.openshift.io/v1 --manifests-dir manifests --verify-only

verify: install-tools verify-crds
	golangci-lint run
	# Remove once https://github.com/golangci/golangci-lint/issues/597 is
	# addressed
	gosec -severity high --confidence medium -exclude G204 -quiet ./...
	hack/verify-codegen.sh
	hack/verify-generated-bindata.sh

# Template for defining build targets for binaries.
define target_template =
 .PHONY: $(1) machine-config-$(1)
 machine-config-$(1): _build-machine-config-$(1)
 $(1): machine-config-$(1)

 mc += $(1)
endef

# Create a target for each component
$(foreach C, $(COMPONENTS), $(eval $(call target_template,$(C))))

# Template for image builds.
define image_template =
 .PHONY: image-$(1) image-machine-config-$(1) deploy-$(1) deploy-machine-config-$(1)
 image-machine-config-$(1): _image-machine-config-$(1) _build-machine-config-$(1)
 image-$(1): image-machine-config-$(1)
 deploy-machine-config-$(1): _deploy-machine-config-$(1)
 deploy-$(1): _deploy-machine-config-$(1)

 imc += image-$(1)
endef

# Generate 'image_template' for each component
$(foreach C, $(COMPONENTS), $(eval $(call image_template,$(C))))

.PHONY: binaries images images.rhel7

# Build all binaries:
# Example:
#    make binaries
binaries: $(mc)

# Build all images:
# Example:
#    make images
images: $(imc)

# Build all images for rhel7
# Example:
#    make images.rhel7
images.rhel7: $(imc7)

# This was copied from https://github.com/openshift/cluster-image-registry-operato
test-e2e:
	go test -timeout 90m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/
