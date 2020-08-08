// +build tools

// tools is a dummy package that will be ignored for builds, but included for dependencies.
package tools

import (
	// Code generators built at runtime.
	_ "github.com/go-bindata/go-bindata/v3/go-bindata"
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"
	_ "github.com/securego/gosec/cmd/gosec"
	_ "k8s.io/code-generator" // Imports non-go generation scripts
	_ "k8s.io/code-generator/cmd/client-gen"
	_ "k8s.io/code-generator/cmd/conversion-gen"
	_ "k8s.io/code-generator/cmd/deepcopy-gen"
	_ "k8s.io/code-generator/cmd/defaulter-gen"
	_ "k8s.io/code-generator/cmd/informer-gen"
	_ "k8s.io/code-generator/cmd/lister-gen"
	// TODO: Investigate openapi-gen
	// _ "k8s.io/code-generator/cmd/openapi-gen"
)
