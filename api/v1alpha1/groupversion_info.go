// Package v1alpha1 contains the API types for the Traefik operator.
// +kubebuilder:object:generate=true
// +groupName=gateway.traefik.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the group version used to register these objects.
var GroupVersion = schema.GroupVersion{Group: "gateway.traefik.io", Version: "v1alpha1"}

// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds the types in this group-version to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &TraefikProxy{}, &TraefikProxyList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
