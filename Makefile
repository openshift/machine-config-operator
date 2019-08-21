COMPONENTS = daemon controller server operator

# vim: noexpandtab ts=8
export GOPATH=$(shell echo $${GOPATH:-$$HOME/go})

.PHONY: clean test test-unit test-e2e verify update
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

# Build image for a component. Intended to be called via another target.
# Example:
#    make _image-machine-config-daemon
_image-%:
	@which podman 2> /dev/null >&1 || { echo "podman must be installed to build an image";  exit 1; }
	WHAT=$* hack/build-image.sh

# Build + push + deploy image for a component. Intended to be called via another target.
# Example:
#    make _deploy-machine-config-daemon
_deploy-%:
	WHAT=$* hack/cluster-push.sh

# Run tests
test: test-unit test-e2e

# Unit tests only (no active cluster required)
test-unit:
	go test -v ./cmd/... ./pkg/... ./lib/...

# Run the code generation tasks.
# Example:
#    make update
update:
	@which go-bindata 2> /dev/null >&1 || { echo "go-bindata must be installed to update generated code";  exit 1; }
	hack/update-codegen.sh
	hack/update-generated-bindata.sh

# Run verification steps
# Example:
#    make verify
verify:
	@which go-bindata 2> /dev/null >&1 || { echo "go-bindata must be installed to verify generated code";  exit 1; }
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
	GOCACHE=off go test -timeout 90m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/
