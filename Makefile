MCO_COMPONENTS = daemon controller server operator
EXTRA_COMPONENTS = apiserver-watcher
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
# set golangci lint cache dir to an accessible location
# this is necessary for running golangci-lint in a container
export GOLANGCI_LINT_CACHE=$(shell echo $${GOLANGCI_LINT_CACHE:-$$GOPATH/cache})

GOTAGS = "containers_image_openpgp exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_ostree_stub"

all: binaries

.PHONY: clean test test-unit test-e2e verify update install-tools
# Remove build artifaces
# Example:
#    make clean
#
clean:
	@rm -rf _output

# Build machine configs. Intended to be called via another target.
# Example:
#    make _build-machine-config-operator
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

go-deps:
	go mod tidy
	go mod vendor
	go mod verify
	# make scripts executable
	chmod +x ./vendor/k8s.io/code-generator/generate-groups.sh
	chmod +x ./vendor/k8s.io/code-generator/generate-internal-groups.sh

install-tools:
	GO111MODULE=on go build -o $(GOPATH)/bin/golangci-lint ./vendor/github.com/golangci/golangci-lint/cmd/golangci-lint

# Run verification steps
# Example:
#    make verify
verify: install-tools
	golangci-lint run --build-tags=$(GOTAGS)
	hack/verify-codegen.sh

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
	 sed -e s,org/openshift/release,org/ocp/builder, -e s,/openshift/origin-v4.0:base,/ocp/4.0:base, < $<) > $@.tmp && mv $@.tmp $@

# This was copied from https://github.com/openshift/cluster-image-registry-operator
test-e2e:
	go test -tags=$(GOTAGS) -failfast -timeout 90m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/

test-e2e-single-node:
	go test -tags=$(GOTAGS) -failfast -timeout 90m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-single-node/

test-e2e-layering:
	go test -tags=$(GOTAGS) -failfast -timeout 90m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-layering/

bootstrap-e2e:
	./hack/bootstrap-e2e-test.sh

bootstrap-e2e-local:
	# Use GOTAGS to exclude the default CGO implementation of signatures, which is not used by MCO
	# but dragged in by containers/image/signature
	CGO_ENABLED=0 go test -tags=$(GOTAGS) -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-bootstrap/
