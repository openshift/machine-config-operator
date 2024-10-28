GO_PKG_NAME := github.com/openshift/machine-config-operator/ginkgo-test

GIT_COMMIT := $(shell git rev-parse --short HEAD)
BUILD_DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
GIT_TREE_STATE := $(shell if git diff --quiet; then echo clean; else echo dirty; fi)

GO_LD_FLAGS := -X '$(GO_PKG_NAME)/pkg/version.CommitFromGit=$(GIT_COMMIT)' \
           -X '$(GO_PKG_NAME)/pkg/version.BuildDate=$(BUILD_DATE)' \
           -X '$(GO_PKG_NAME)/pkg/version.GitTreeState=$(GIT_TREE_STATE)' \
					 -s -w

OUTPUT_DIR := "bin"

.PHONY: build test smoke-test level0-test clean mco-tests

all: clean update build smoke-test

build: mco-tests

mco-tests:
	mkdir -p $(OUTPUT_DIR)
	# GO_COMPLIANCE_POLICY="exempt_all" must only be used for test related binaries.
	# It prevents various FIPS compliance policies from being applied to this compilation.
	# Do not set globally.
	GO_COMPLIANCE_POLICY="exempt_all" GO111MODULE="on" GOWORK=off go build -ldflags="${GO_LD_FLAGS}" -mod=mod -o "${OUTPUT_DIR}" "./cmd/machine-config-test-extension"

smoke-test: build
	$(OUTPUT_DIR)/machine-config-test-extension run-suite mco/smoke-test

level0-test: build
	$(OUTPUT_DIR)/machine-config-test-extension run-suite mco/level0

check-code:
	./hack/check-code.sh master

go-mod-tidy:
	./hack/go-mod-tidy.sh

clean:
	rm -f $(OUTPUT_DIR)/machine-config-test-extension

# Include the library makefile
include $(addprefix ./, bindata.mk)

# This will call a macro called "add-bindata" which will generate bindata specific targets based on the parameters:
# $0 - macro name
# $1 - target suffix
# $2 - input dirs
# $3 - prefix
# $4 - pkg
# $5 - output
# It will generate targets {update,verify}-bindata-$(1) logically grouping them in unsuffixed versions of these targets
# and also hooked into {update,verify}-generated for broader integration.
$(call add-bindata,testdata,test/extended/testdata/...,testextended,testdata,./test/extended/testdata/bindata.go)
