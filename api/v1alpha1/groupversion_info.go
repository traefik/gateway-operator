// Package v1alpha1 contains the API types for the Traefik operator.
// +kubebuilder:object:generate=true
// +groupName=gateway.traefik.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the group version used to register these objects.
var GroupVersion = schema.GroupVersion{Group: "gateway.traefik.io", Version: "v1alpha1"}

// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme adds the types in this group-version to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme
