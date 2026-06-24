package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TraefikProxySpec defines the data plane provisioned for a Gateway.
// It is referenced from GatewayClass.spec.parametersRef, mirroring the
// EnvoyProxy model of Envoy Gateway.
type TraefikProxySpec struct {
	// Deployment configures the data plane Deployment.
	// +optional
	Deployment *DeploymentConfig `json:"deployment,omitempty"`

	// Service configures the data plane Service.
	// +optional
	Service *ServiceConfig `json:"service,omitempty"`

	// Provisioning controls where and how the data plane is deployed.
	// +optional
	Provisioning *ProvisioningConfig `json:"provisioning,omitempty"`

	// MergeGateways makes all Gateways of the owning GatewayClass share a
	// single data plane. Not implemented before phase 3.
	// +optional
	MergeGateways bool `json:"mergeGateways,omitempty"`
}

// DeploymentMode selects where the data plane resource set is created.
// +kubebuilder:validation:Enum=GatewayNamespace;Dedicated
type DeploymentMode string

const (
	// GatewayNamespaceMode provisions the data plane in the Gateway's own
	// namespace, with the Gateway as owner for garbage collection.
	GatewayNamespaceMode DeploymentMode = "GatewayNamespace"

	// DedicatedMode provisions the data plane in a single shared namespace.
	// Cross-namespace ownership is not expressible, so the operator uses a
	// finalizer on the Gateway and deletes the resources explicitly.
	DedicatedMode DeploymentMode = "Dedicated"
)

// ProvisioningConfig controls the data plane deployment topology.
type ProvisioningConfig struct {
	// Mode selects the deployment topology. Defaults to GatewayNamespace.
	// +optional
	Mode DeploymentMode `json:"mode,omitempty"`

	// Namespace is the target namespace for the data plane when Mode is
	// Dedicated. When empty, the operator default namespace is used.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// DeploymentConfig configures the provisioned data plane Deployment.
type DeploymentConfig struct {
	// Replicas is the number of data plane replicas.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Image is the Traefik container image to run.
	// +optional
	Image string `json:"image,omitempty"`

	// Resources are the container resource requirements.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// PodAnnotations are added to the data plane pods.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// PodLabels are added to the data plane pods.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`
}

// ServiceConfig configures the provisioned data plane Service.
type ServiceConfig struct {
	// Type is the Service type used to expose the data plane.
	// +kubebuilder:validation:Enum=LoadBalancer;ClusterIP;NodePort
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Annotations are added to the data plane Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// LoadBalancerClass selects the load balancer implementation when Type
	// is LoadBalancer.
	// +optional
	LoadBalancerClass *string `json:"loadBalancerClass,omitempty"`
}

// TraefikProxyStatus is the observed state of a TraefikProxy.
type TraefikProxyStatus struct {
	// Conditions describe the current state of the TraefikProxy.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=tp

// TraefikProxy is the data plane configuration referenced by a GatewayClass.
type TraefikProxy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TraefikProxySpec   `json:"spec,omitempty"`
	Status TraefikProxyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TraefikProxyList contains a list of TraefikProxy.
type TraefikProxyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TraefikProxy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TraefikProxy{}, &TraefikProxyList{})
}
