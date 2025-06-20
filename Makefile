E2E_ROOT_DIR = ./test
E2E_SUITES = $(notdir $(wildcard $(E2E_ROOT_DIR)/e2e*))

MCO_COMPONENTS = daemon controller server operator
EXTRA_COMPONENTS = apiserver-watcher machine-os-builder
ALL_COMPONENTS = $(patsubst %,machine-config-%,$(MCO_COMPONENTS)) $(EXTRA_COMPONENTS)
ALL_COMPONENTS_PATHS = $(patsubst %,cmd/%,$(ALL_COMPONENTS))
PREFIX ?= /usr
GO111MODULE?=on
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))

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
#    make _build-component-machine-config-operator
_build-component-%:
	WHAT_PATH=cmd/$* WHAT=$(basename $*) hack/build-go.sh

# Build the helpers under devex/cmd.
_build-helper-%:
	WHAT_PATH=devex/cmd/$* WHAT=$(basename $*) hack/build-go.sh

# Verify that an e2e test is valid Golang by doing a trial compilation.
_verify-e2e-%:
	go test -c -tags=$(GOTAGS) -o _output/$* ./test/$*/...

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
	hack/update-templates.sh
go-deps:
	go mod tidy
	go mod vendor
	go mod verify
	# make scripts executable
	chmod +x ./vendor/k8s.io/code-generator/generate-groups.sh
	chmod +x ./vendor/k8s.io/code-generator/generate-internal-groups.sh

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31.1
ENVTEST = go run ${PROJECT_DIR}/vendor/sigs.k8s.io/controller-runtime/tools/setup-envtest
SETUP_ENVTEST := $(shell command -v setup-envtest 2> /dev/null)
install-setup-envtest:
ifdef SETUP_ENVTEST
	@echo "Found setup-envtest"
else
	@echo "Installing setup-envtest"
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest
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

install-tools: install-golangci-lint install-go-junit-report install-setup-envtest

# Runs golangci-lint
lint: install-tools
	./hack/golangci-lint.sh $(GOTAGS)

# Verifies templates.
verify-templates:
	hack/verify-templates.sh

# Verifies devex helpers
verify-helpers:
	# Conditionally tries to build the helper binaries in CI.
	hack/verify-helpers.sh

# Runs all verification steps
# Example:
#    make verify
verify: install-tools verify-e2e lint verify-templates verify-helpers

HELPERS_DIR := devex/cmd
HELPER_BINARIES := $(notdir $(wildcard $(HELPERS_DIR)/*))

.PHONY: helpers
helpers: $(patsubst %,_build-helper-%,$(HELPER_BINARIES))

# Template for defining build targets for binaries.
define target_template =
 .PHONY: $(1)
 $(1): _build-component-$(1)
endef
# Create a target for each component
$(foreach C, $(EXTRA_COMPONENTS), $(eval $(call target_template,$(C))))
$(foreach C, $(MCO_COMPONENTS), $(eval $(call target_template,$(patsubst %,machine-config-%,$(C)))))

# Template for defining build targets for helper binaries.
define helper_target_template =
 .PHONY: $(1)
 $(1): _build-helper-$(1)
endef
# Create a target for each component
$(foreach C, $(HELPER_BINARIES), $(eval $(call helper_target_template,$(C))))

define verify_e2e_target_template =
 .PHONY: $(1)
 $(1): _verify-e2e-$(1)
endef
# Create a target for each e2e suite
$(foreach C, $(E2E_SUITES), $(eval $(call verify_e2e_target_template,$(C))))


.PHONY: binaries helpers install

# Build all binaries:
# Example:
#    make binaries
binaries: $(patsubst %,_build-component-%,$(ALL_COMPONENTS))

# Installs the helper binaries from devex/cmd.
install-helpers: helpers
	for helper in $(HELPER_BINARIES); do \
		install -D -m 0755 _output/linux/$(GOARCH)/$${helper} $(DESTDIR)$(PREFIX)/bin/$${helper}; \
	done

install: binaries
	for component in $(ALL_COMPONENTS); do \
	  install -D -m 0755 _output/linux/$(GOARCH)/$${component} $(DESTDIR)$(PREFIX)/bin/$${component}; \
	done

Dockerfile.rhel7: Dockerfile Makefile
	(echo '# THIS FILE IS GENERATED FROM '$<' DO NOT EDIT' && \
	 sed -e s,org/openshift/release,org/ocp/builder, -e s,/openshift/origin-v4.0:base,/ocp/4.0:base, < $<) > $@.tmp && mv $@.tmp $@

# Validates that all of the e2e test suites are valid Golang by performing a test compilation.
verify-e2e: $(patsubst %,_verify-e2e-%,$(E2E_SUITES))

# This was copied from https://github.com/openshift/cluster-image-registry-operator
test-e2e: install-go-junit-report
	set -o pipefail; go test -tags=$(GOTAGS) -failfast -timeout 190m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/ ./test/e2e-techpreview-shared/ | ./hack/test-with-junit.sh $(@)

test-e2e-techpreview: install-go-junit-report
	set -o pipefail; go test -tags=$(GOTAGS) -failfast -timeout 170m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-techpreview  ./test/e2e-techpreview-shared/ | ./hack/test-with-junit.sh $(@)

test-e2e-single-node: install-go-junit-report
	set -o pipefail; go test -tags=$(GOTAGS) -failfast -timeout 120m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-single-node/ | ./hack/test-with-junit.sh $(@)

test-e2e-ocl: install-go-junit-report
	set -o pipefail; go test -tags=$(GOTAGS) -failfast -timeout 120m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-ocl/ | ./hack/test-with-junit.sh $(@)

bootstrap-e2e: install-go-junit-report install-setup-envtest
	@echo "Setting up KUBEBUILDER_ASSETS"
	@KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --index https://raw.githubusercontent.com/openshift/api/master/envtest-releases.yaml --bin-dir $(PROJECT_DIR)/bin -p path)" && \
	echo "KUBEBUILDER_ASSETS=$$KUBEBUILDER_ASSETS" && \
	set -o pipefail && \
	KUBEBUILDER_ASSETS=$$KUBEBUILDER_ASSETS go test -tags=$(GOTAGS) -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-bootstrap/ | ./hack/test-with-junit.sh $(@)
