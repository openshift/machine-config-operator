MCO_COMPONENTS = daemon controller server operator
EXTRA_COMPONENTS = apiserver-watcher machine-os-builder
ALL_COMPONENTS = $(patsubst %,machine-config-%,$(MCO_COMPONENTS)) $(EXTRA_COMPONENTS)
PREFIX ?= /usr
GO111MODULE?=on

GOTESTSUM_FORMAT = standard-verbose

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

GOTESTSUM_OPTS = --format $(GOTESTSUM_FORMAT) --junitfile "$(ARTIFACT_DIR)/junit-$(@).xml"
UNITTEST_TARGETS = ./cmd/... ./pkg/... ./lib/... ./test/helpers/...
UNITTEST_OPTS = -v -count=1 -tags=$(GOTAGS)

all: binaries

.PHONY: clean test test-unit test-unit-with-coverage test-e2e verify update install-tools

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

ARTIFACT_DIR ?= $(CURDIR)

# Unit tests only (no active cluster required)
test-unit-old: install-gotestsum
	gotestsum $(GOTESTSUM_OPTS) -- $(UNITTEST_OPTS) $(UNITTEST_TARGETS)

test-unit: install-gotestsum
	gotestsum $(GOTESTSUM_OPTS) -- $(UNITTEST_OPTS) -coverprofile="$(ARTIFACT_DIR)/mco-unit-test-coverage.out" $(UNITTEST_TARGETS)
	go tool cover -html="$(ARTIFACT_DIR)/mco-unit-test-coverage.out" -o "$(ARTIFACT_DIR)/mco-unit-test-coverage.html"

# Run the code generation tasks.
# Example:
#    make update
update:
	hack/update-templates.sh
	hack/crds-sync.sh
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
	go install -mod= sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.0.0-20240315194348-5aaf1190f880
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

GOTESTSUM := $(shell command -v gotestsum 2> /dev/null)
install-gotestsum:
ifdef GOTESTSUM
	@echo "Found gotestsum"
	gotestsum --version
	which gotestsum
else
	@echo "Installing gotestsum"
	curl -L ""https://github.com/gotestyourself/gotestsum/releases/download/v1.12.0/gotestsum_1.12.0_linux_$(GOARCH).tar.gz" | tar zxv gotestsum && mv gotestsum $(GOPATH)/bin/gotestsum
endif

install-tools: install-golangci-lint install-setup-envtest install-gotestsum

# Run verification steps
# Example:
#    make verify
verify: install-tools
	./hack/golangci-lint.sh $(GOTAGS)
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
test-e2e: install-gotestsum
	gotestsum $(GOTESTSUM_OPTS) -- -tags=$(GOTAGS) -failfast -timeout 170m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e/ ./test/e2e-techpreview-shared/

test-e2e-techpreview: install-gotestsum
	gotestsum $(GOTESTSUM_OPTS) -- -tags=$(GOTAGS) -failfast -timeout 170m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-techpreview  ./test/e2e-techpreview-shared/

test-e2e-single-node: install-gotestsum
	gotestsum $(GOTESTSUM_OPTS) -- -tags=$(GOTAGS) -failfast -timeout 120m -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-single-node/

bootstrap-e2e: install-setup-envtest install-gotestsum
	gotestsum $(GOTESTSUM_OPTS) -- -tags=$(GOTAGS) -v$${WHAT:+ -run="$$WHAT"} ./test/e2e-bootstrap/
