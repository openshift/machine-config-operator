MCO_COMPONENTS = daemon controller server operator
EXTRA_COMPONENTS = apiserver-watcher machine-os-builder
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
test-unit: install-go-junit-report
	./hack/test-unit.sh $(@) $(GOTAGS)

# Run the code generation tasks.
# Example:
#    make update
update:
	hack/update-codegen.sh
	hack/update-templates.sh

go-deps:
	go mod tidy
	go mod vendor
	go mod verify
	# make scripts executable
	chmod +x ./vendor/k8s.io/code-generator/generate-groups.sh
	chmod +x ./vendor/k8s.io/code-generator/generate-internal-groups.sh

SETUP_ENVTEST := $(shell command -v setup-envtest 2> /dev/null)
install-setup-envtest:
ifdef SETUP_ENVTEST
	@echo "Found setup-envtest"
else
	@echo "Installing setup-envtest"
	go install -mod= sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
endif

GO_JUNIT_REPORT := $(shell command -v go-junit-report 2> /dev/null)
install-go-junit-report:
ifdef GO_JUNIT_REPORT
	@echo "Found go-junit-report"
	go-junit-report --version
else
	@echo "Installing go-junit-report"
	go install -mod= github.com/jstemmer/go-junit-report@latest
endif

GOLANGCI_LINT := $(shell command -v golangci-lint 2> /dev/null)
install-golangci-lint:
ifdef GOLANGCI_LINT
	@echo "Found golangci-lint"
	golangci-lint --version
else
	@echo "Installing golangci-lint"
	GO111MODULE=on go build -o $(GOPATH)/bin/golangci-lint ./vendor/github.com/golangci/golangci-lint/cmd/golangci-lint
endif

install-tools: install-golangci-lint install-setup-envtest install-go-junit-report

# Run verification steps
# Example:
#    make verify
verify: install-tools
	./hack/golangci-lint.sh $(GOTAGS)
	hack/verify-codegen.sh
	hack/verify-templates.sh

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
test-e2e: install-go-junit-report
	set -o pipefail; go test -tags=$(GOTAGS) -failfast -timeout 150m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/ | ./hack/test-with-junit.sh $(@)

test-e2e-single-node: install-go-junit-report
	set -o pipefail; go test -tags=$(GOTAGS) -failfast -timeout 120m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-single-node/ | ./hack/test-with-junit.sh $(@)

bootstrap-e2e: install-go-junit-report install-setup-envtest
	set -o pipefail; CGO_ENABLED=0 go test -tags=$(GOTAGS) -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-bootstrap/ | ./hack/test-with-junit.sh $(@)
