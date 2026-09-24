package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

	// Autoscaling configures a HorizontalPodAutoscaler for the data plane. When
	// set, the operator does not manage the Deployment replica count.
	// +optional
	Autoscaling *AutoscalingConfig `json:"autoscaling,omitempty"`

	// PodDisruptionBudget configures a PodDisruptionBudget for the data plane.
	// +optional
	PodDisruptionBudget *PodDisruptionBudgetConfig `json:"podDisruptionBudget,omitempty"`

	// Logs configures the data plane logging.
	// +optional
	Logs *LogsConfig `json:"logs,omitempty"`

	// Metrics configures the data plane metrics.
	// +optional
	Metrics *MetricsConfig `json:"metrics,omitempty"`

	// Tracing configures the data plane tracing.
	// +optional
	Tracing *TracingConfig `json:"tracing,omitempty"`
}

// AutoscalingConfig configures a HorizontalPodAutoscaler for the data plane.
type AutoscalingConfig struct {
	// MinReplicas is the lower bound for the number of replicas.
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the upper bound for the number of replicas.
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`

	// TargetCPUUtilizationPercentage is the average CPU target across pods.
	// +optional
	TargetCPUUtilizationPercentage *int32 `json:"targetCPUUtilizationPercentage,omitempty"`
}

// PodDisruptionBudgetConfig configures a PodDisruptionBudget for the data plane.
type PodDisruptionBudgetConfig struct {
	// MinAvailable is the minimum number of available pods.
	// +optional
	MinAvailable *intstr.IntOrString `json:"minAvailable,omitempty"`

	// MaxUnavailable is the maximum number of unavailable pods.
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// LogsConfig configures the data plane logging.
type LogsConfig struct {
	// Level is the log level (DEBUG, INFO, WARN, ERROR).
	// +optional
	Level string `json:"level,omitempty"`
}

// MetricsConfig configures the data plane metrics.
type MetricsConfig struct {
	// Prometheus enables the Prometheus metrics endpoint on a dedicated
	// "metrics" entry point.
	// +optional
	Prometheus bool `json:"prometheus,omitempty"`
}

// TracingConfig configures the data plane tracing.
type TracingConfig struct {
	// OTLPEndpoint enables OTLP tracing to the given HTTP endpoint.
	// +optional
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`
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

	Items []TraefikProxy `json:"items"`
}
