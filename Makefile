MCO_COMPONENTS = daemon controller server operator
EXTRA_COMPONENTS = setup-etcd-environment gcp-routes-controller
ALL_COMPONENTS = $(patsubst %,machine-config-%,$(MCO_COMPONENTS)) $(EXTRA_COMPONENTS)
PREFIX ?= /usr
GO111MODULE?=on

# Copied from coreos-assembler
GOARCH := $(shell uname -m)
ifeq ($(GOARCH),x86_64)
	GOARCH = amd64
else ifeq ($(GOARCH),aarch64)
	GOARCH = arm64
endif

# vim: noexpandtab ts=8
export GOPATH=$(shell echo $${GOPATH:-$$HOME/go})
export GO111MODULE
export GOPROXY=https://proxy.golang.org

GOTAGS = "containers_image_openpgp exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_ostree_stub"

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

# Run tests
test: test-unit test-e2e

# Unit tests only (no active cluster required)
test-unit:
	CGO_ENABLED=0 go test -tags=$(GOTAGS) -count=1 -v ./cmd/... ./pkg/... ./lib/...

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
	# make scripts executable
	chmod +x ./vendor/k8s.io/code-generator/generate-groups.sh
	chmod +x ./vendor/k8s.io/code-generator/generate-internal-groups.sh

install-tools:
	GO111MODULE=on go build -o $(GOPATH)/bin/golangci-lint ./vendor/github.com/golangci/golangci-lint/cmd/golangci-lint
	GO111MODULE=on go build -o $(GOPATH)/bin/gosec ./vendor/github.com/securego/gosec/cmd/gosec

# Run verification steps
# Example:
#    make verify
verify: install-tools
	golangci-lint run --build-tags=$(GOTAGS)
	# Remove once https://github.com/golangci/golangci-lint/issues/597 is
	# addressed
	gosec -severity high --confidence medium -exclude G204 -quiet ./...
	# Remove the vendor/k8s.io/code-generator vendor hack
	# once code-generator plays nice with go modules, see
	# https://github.com/kubernetes/kubernetes/issues/82531 and
	# https://github.com/kubernetes/kubernetes/pull/85559
	pushd vendor/k8s.io/code-generator && cp go.mod go.mod.bak && go mod vendor && popd
	hack/verify-codegen.sh
	hack/verify-generated-bindata.sh
	rm -f vendor/k8s.io/code-generator/go.mod
	mv vendor/k8s.io/code-generator/go.mod.bak vendor/k8s.io/code-generator/go.mod
	rm -rf vendor/k8s.io/code-generator/vendor

# Template for defining build targets for binaries.
define target_template =
 .PHONY: $(1)
 $(1): _build-$(1)
endef
# Create a target for each component
$(foreach C, $(EXTRA_COMPONENTS), $(eval $(call target_template,$(C))))
$(foreach C, $(MCO_COMPONENTS), $(eval $(call target_template,$(patsubst %,machine-config-%,$(C)))))

.PHONY: binaries install

# Build all binaries:
# Example:
#    make binaries
binaries: $(patsubst %,_build-%,$(ALL_COMPONENTS))

install: binaries
	for component in $(ALL_COMPONENTS); do \
	  install -D -m 0755 _output/linux/$(GOARCH)/$${component} $(DESTDIR)$(PREFIX)/bin/$${component}; \
	done

Dockerfile.rhel7: Dockerfile Makefile
	(echo '# THIS FILE IS GENERATED FROM '$<' DO NOT EDIT' && \
	 sed -e s,org/openshift/release,org/ocp/builder, -e s,/origin/4.5:base,/ocp/4.5:base, < $<) > $@.tmp && mv $@.tmp $@

# This was copied from https://github.com/openshift/cluster-image-registry-operator
test-e2e:
	go test -failfast -timeout 120m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/
