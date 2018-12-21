COMPONENTS = daemon controller server operator

# vim: noexpandtab ts=8
my_p=$(shell pwd -P)
export GOPATH=$(shell echo $${GOPATH:-$$HOME/go})

.PHONY: clean test verify update
# Remove build artifaces
# Example:
#    make clean
#
clean:
	@rm -rf $(my_p)/_output

# Build machine configs. Intended to be called via another target.
# Example:
#    make _build-setup-etcd
_build-%:
	WHAT=$* $(my_p)/hack/build-go.sh

# Build image for a component. Intended to be called via another target.
# Example:
#    make _image-machine-config-daemon
_image-%:
	@which podman 2> /dev/null &>1 || { echo "podman must be installed to build an image";  exit 1; }
	WHAT=$* $(my_p)/hack/build-image.sh

# Build + push + deploy image for a component. Intended to be called via another target.
# Example:
#    make _deploy-machine-config-daemon
_deploy-%:
	WHAT=$* $(my_p)/hack/cluster-push.sh

# Run unit tests
# Example:
#    make test
test:
	cd $(my_p) && go test -v ./...

# Run the code generation tasks.
# Example:
#    make update
update:
	$(my_p)/hack/update-codegen.sh

# Run verification steps
# Example:
#    make verify
verify: test machine-configs
	$(my_p)/hack/verify-style.sh
	$(my_p)/hack/verify-codegen.sh

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

.PHONY: machine-configs images images.rhel7

# Build all machine-configs:
# Example:
#    make machine-configs
machine-configs: $(mc)

# Build all images:
# Example:
#    make images
images: $(imc)

# Build all images for rhel7
# Example:
#    make images.rhel7
images.rhel7: $(imc7)
