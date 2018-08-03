# Update all generated artifacts.
#
# Example:
#   make update
update:
	hack/update-codegen.sh

# Build and run the complete test-suite.
#
# Example:
#   make test
test: test-unit
.PHONY: test

# Run unit tests.
#
# Example:
#   make test-unit
test-unit:
	go test ./...
.PHONY: test-unit

# Build code.
#
# Args:
#   WHAT: names to build. Builds package cmd/${WHAT}
#   GOFLAGS: Extra flags to pass to 'go' when building.
#
# Example:
#   make
#   make all
#   make all WHAT=machine-config-operator GOFLAGS=-v
all build:
	hack/build-go.sh $(WHAT) $(GOFLAGS)
.PHONY: all build

# Verify code conventions are properly setup.
#
# Example:
#   make verify
verify: build
	hack/verify-style.sh
	hack/verify-codegen.sh

# Run core verification and all self contained tests.
#
# Example:
#   make check
check: | build verify
	$(MAKE) test-unit -o build -o verify
.PHONY: check

# Remove all build artifacts.
#
# Example:
#   make clean
clean:
	rm -rf _output
.PHONY: clean