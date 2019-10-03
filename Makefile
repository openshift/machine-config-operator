COMPONENTS = daemon controller server operator
GO111MODULE?=on

# vim: noexpandtab ts=8
export GOPATH=$(shell echo $${GOPATH:-$$HOME/go})
export GO111MODULE
export GOPROXY=https://proxy.golang.org

GOFLAGS = "containers_image_openpgp exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_ostree_stub"

# grab the version from a dummy pkg in k8s.io/code-generator from vendor/modules.txt (read by go list)
versionPath=$(shell GO111MODULE=on go list -f {{.Dir}} k8s.io/code-generator/cmd/client-gen)
codegeneratorRoot=$(versionPath:/cmd/client-gen=)
codegeneratorTarget:=./vendor/k8s.io/code-generator

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
	go test -tags=$(GOFLAGS) -count=1 -v ./cmd/... ./pkg/... ./lib/...

# Run the code generation tasks.
# Example:
#    make update
update:
	hack/update-codegen.sh
	hack/update-generated-bindata.sh

go-deps:
	go mod tidy
	go mod vendor
	go mod verify
	# go mod does not vendor in scripts so we need to get them manually...
	@mkdir -p $(codegeneratorRoot)
	@cp $(codegeneratorRoot)/generate-groups.sh $(codegeneratorTarget) && chmod +x $(codegeneratorTarget)/generate-groups.sh
	@cp $(codegeneratorRoot)/generate-internal-groups.sh $(codegeneratorTarget) && chmod +x $(codegeneratorTarget)/generate-internal-groups.sh

install-tools:
	# mktemp -d is required to avoid the creation of go modules related files in the project root
	cd $(shell mktemp -d) && GO111MODULE=on go get github.com/securego/gosec/cmd/gosec@4b59c948083cd711b6a8aac8f32721b164899f57
	#cd $(shell mktemp -d) && GO111MODULE=on go get github.com/golangci/golangci-lint/cmd/golangci-lint@v1.18.0
	GO111MODULE=on go build -o $(GOPATH)/bin/golangci-lint -mod=vendor ./vendor/github.com/golangci/golangci-lint/cmd/golangci-lint

# Run verification steps
# Example:
#    make verify
verify: install-tools
	golangci-lint run --build-tags=$(GOFLAGS)
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
	go test -timeout 120m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/
