// Package v1alpha1 contains API Schema definitions for the aro v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=aro.openshift.io
package v1alpha1

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "aro.openshift.io", Version: "v1alpha1"}

	// SchemeGroupVersion manual hack
	SchemeGroupVersion = GroupVersion

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
