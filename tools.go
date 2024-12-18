//go:build tools
// +build tools

// tools is a dummy package that will be ignored for builds, but included for dependencies.
package tools

import (
	// Code generators built at runtime.
	_ "github.com/openshift/api/config/v1alpha1/zz_generated.crd-manifests"
	_ "github.com/openshift/api/machineconfiguration/v1/zz_generated.crd-manifests"
	_ "github.com/openshift/api/machineconfiguration/v1alpha1/zz_generated.crd-manifests"
	_ "github.com/openshift/api/operator/v1/zz_generated.crd-manifests"
	_ "k8s.io/code-generator" // TODO: Investigate why scripts in this directory are removed and not vendored by go mod.
	_ "k8s.io/code-generator/cmd/client-gen"
	_ "k8s.io/code-generator/cmd/conversion-gen"
	_ "k8s.io/code-generator/cmd/deepcopy-gen"
	_ "k8s.io/code-generator/cmd/defaulter-gen"
	_ "k8s.io/code-generator/cmd/informer-gen"
	_ "k8s.io/code-generator/cmd/lister-gen"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"

	// TODO: Investigate openapi-gen
	// _ "k8s.io/code-generator/cmd/openapi-gen"

	_ "github.com/containers/kubensmnt/utils/systemd"
)
