package provisioner

import (
	"fmt"
	"maps"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/gateway-operator/api/v1alpha1"
)

// metricsPort is the container port exposing Prometheus metrics.
const metricsPort int32 = 9100

// DataPlaneClusterRole is the name of the static ClusterRole the operator binds
// to each provisioned data plane ServiceAccount. It carries the read and status
// permissions the in-tree kubernetesGateway provider requires, and is shipped
// with the operator install (config/rbac/dataplane_role.yaml).
const DataPlaneClusterRole = "traefik-gateway-dataplane"

// buildParams describes a data plane resource set to render.
type buildParams struct {
	name        string
	namespace   string
	selector    map[string]string
	labels      map[string]string // applied to every resource (includes selector)
	annotations map[string]string
	ports       []int32
	refs        []types.NamespacedName // gateway scoping references "namespace/name"
	proxy       *v1alpha1.TraefikProxy
}

// ResourceSet is the data plane resource set provisioned for one Gateway.
// PodDisruptionBudget and HorizontalPodAutoscaler are nil unless configured.
type ResourceSet struct {
	ServiceAccount      *corev1.ServiceAccount
	ClusterRoleBinding  *rbacv1.ClusterRoleBinding
	Deployment          *appsv1.Deployment
	Service             *corev1.Service
	PodDisruptionBudget *policyv1.PodDisruptionBudget
	HPA                 *autoscalingv2.HorizontalPodAutoscaler
}

// Render builds the desired data plane resource set for a single Gateway,
// applying the resolved TraefikProxy configuration. The listenerSets are the
// ListenerSets attached to the Gateway, whose listeners are served by the same
// data plane. Resources are rendered in namespace. Owner references are set by
// the caller.
func Render(gw *gwapiv1.Gateway, listenerSets []gwapiv1.ListenerSet, proxy *v1alpha1.TraefikProxy, namespace string) *ResourceSet {
	set := build(buildParams{
		name:        ResourceName(gw),
		namespace:   namespace,
		selector:    SelectorLabels(gw),
		labels:      commonLabels(gw),
		annotations: infraAnnotations(gw),
		ports:       listenerPorts(gw, listenerSets),
		refs:        []types.NamespacedName{{Namespace: gw.Namespace, Name: gw.Name}},
		proxy:       proxy,
	})
	if proxy != nil && proxy.Spec.Service != nil {
		set.Service.Annotations = mergeMaps(set.Service.Annotations, proxy.Spec.Service.Annotations)
	}
	applyStaticAddresses(set.Service, gw)
	return set
}

// SelectorLabels identify the data plane resources of a Gateway. They are
// stable and must not include propagated infrastructure labels (those can
// change). The Gateway name is shortened when it exceeds the label value limit.
func SelectorLabels(gw *gwapiv1.Gateway) map[string]string {
	return map[string]string{
		GatewayNameLabel:      LabelValue(gw.Name),
		GatewayNamespaceLabel: gw.Namespace,
		ManagedByLabel:        ManagedByValue,
	}
}

func build(p buildParams) *ResourceSet {
	om := metav1.ObjectMeta{
		Name:        p.name,
		Namespace:   p.namespace,
		Labels:      p.labels,
		Annotations: p.annotations,
	}

	sa := &corev1.ServiceAccount{ObjectMeta: om}

	// Cluster-scoped binding granting the data plane ServiceAccount the
	// gateway-provider read/status permissions. Cluster-scoped, so it carries
	// no namespace and is reclaimed by the Gateway finalizer.
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: p.name, Labels: p.labels},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     DataPlaneClusterRole,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      p.name,
			Namespace: p.namespace,
		}},
	}

	dep := &appsv1.Deployment{
		ObjectMeta: om,
		Spec: appsv1.DeploymentSpec{
			Replicas: deploymentReplicas(p.proxy),
			Selector: &metav1.LabelSelector{MatchLabels: p.selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					// Pods carry the full label/annotation set (including
					// propagated spec.infrastructure metadata, required by the
					// GatewayInfrastructure conformance test). The selector stays
					// the stable subset.
					Labels:      mergeMaps(p.labels, podLabels(p.proxy)),
					Annotations: mergeMaps(p.annotations, podAnnotations(p.proxy)),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: p.name,
					Containers: []corev1.Container{{
						Name:      "traefik",
						Image:     image(p.proxy),
						Args:      containerArgs(p),
						Ports:     containerPorts(p.ports, p.proxy),
						Resources: resources(p.proxy),
					}},
				},
			},
		},
	}

	svc := &corev1.Service{
		ObjectMeta: om,
		Spec: corev1.ServiceSpec{
			Type:              serviceType(p.proxy),
			Selector:          p.selector,
			Ports:             servicePorts(p.ports),
			LoadBalancerClass: loadBalancerClass(p.proxy),
		},
	}

	return &ResourceSet{
		ServiceAccount:      sa,
		ClusterRoleBinding:  crb,
		Deployment:          dep,
		Service:             svc,
		PodDisruptionBudget: buildPDB(p, om),
		HPA:                 buildHPA(p, om),
	}
}

// deploymentReplicas returns the desired replica count, or nil when an
// autoscaler manages it.
func deploymentReplicas(proxy *v1alpha1.TraefikProxy) *int32 {
	if proxy != nil && proxy.Spec.Autoscaling != nil {
		return nil
	}
	return replicas(proxy)
}

func buildPDB(p buildParams, om metav1.ObjectMeta) *policyv1.PodDisruptionBudget {
	if p.proxy == nil || p.proxy.Spec.PodDisruptionBudget == nil {
		return nil
	}
	cfg := p.proxy.Spec.PodDisruptionBudget
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: om,
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable:   cfg.MinAvailable,
			MaxUnavailable: cfg.MaxUnavailable,
			Selector:       &metav1.LabelSelector{MatchLabels: p.selector},
		},
	}
}

func buildHPA(p buildParams, om metav1.ObjectMeta) *autoscalingv2.HorizontalPodAutoscaler {
	if p.proxy == nil || p.proxy.Spec.Autoscaling == nil {
		return nil
	}
	cfg := p.proxy.Spec.Autoscaling
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: om,
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       p.name,
			},
			MinReplicas: cfg.MinReplicas,
			MaxReplicas: cfg.MaxReplicas,
		},
	}
	if cfg.TargetCPUUtilizationPercentage != nil {
		hpa.Spec.Metrics = []autoscalingv2.MetricSpec{{
			Type: autoscalingv2.ResourceMetricSourceType,
			Resource: &autoscalingv2.ResourceMetricSource{
				Name: corev1.ResourceCPU,
				Target: autoscalingv2.MetricTarget{
					Type:               autoscalingv2.UtilizationMetricType,
					AverageUtilization: cfg.TargetCPUUtilizationPercentage,
				},
			},
		}}
	}
	return hpa
}

// applyStaticAddresses requests the Gateway spec.addresses on the provisioned
// Service. IP addresses are set as ExternalIPs (works for every Service type
// and supports multiple addresses); for a LoadBalancer the first IP is also set
// as the legacy LoadBalancerIP for clouds that honor it. Hostname addresses are
// not bindable on a Service and are left for the status logic to report as not
// usable. Only supported address types are rendered.
func applyStaticAddresses(svc *corev1.Service, gw *gwapiv1.Gateway) {
	var ips []string
	for _, a := range gw.Spec.Addresses {
		if a.Type != nil && *a.Type != gwapiv1.IPAddressType {
			continue
		}
		ips = append(ips, a.Value)
	}
	if len(ips) == 0 {
		return
	}
	svc.Spec.ExternalIPs = ips
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		svc.Spec.LoadBalancerIP = ips[0]
	}
}

// commonLabels carries the operator markers plus the propagated
// spec.infrastructure.labels (GatewayInfrastructurePropagation).
func commonLabels(gw *gwapiv1.Gateway) map[string]string {
	l := SelectorLabels(gw)
	if gw.Spec.Infrastructure != nil {
		for k, v := range gw.Spec.Infrastructure.Labels {
			l[string(k)] = string(v)
		}
	}
	return l
}

// infraAnnotations carries the propagated spec.infrastructure.annotations.
func infraAnnotations(gw *gwapiv1.Gateway) map[string]string {
	if gw.Spec.Infrastructure == nil || len(gw.Spec.Infrastructure.Annotations) == 0 {
		return nil
	}
	a := map[string]string{}
	for k, v := range gw.Spec.Infrastructure.Annotations {
		a[string(k)] = string(v)
	}
	return a
}

// containerArgs builds the data plane Traefik arguments. The kubernetesGateway
// provider is scoped to the data plane's Gateway(s), reports the address of the
// Service the operator provisioned for it, and writes its own Gateway, listener
// and route status. The operator only writes the Gateway status when a static
// address is rejected for an unsupported type (in which case no data plane
// runs); otherwise the data plane owns it. See DESIGN.md section 8.
func containerArgs(p buildParams) []string {
	proxy := p.proxy
	args := []string{
		"--providers.kubernetesGateway=true",
		"--providers.kubernetesGateway.experimentalChannel=true",
		"--providers.kubernetesGateway.statusAddress.service.name=" + p.name,
		"--providers.kubernetesGateway.statusAddress.service.namespace=" + p.namespace,
	}
	for i, ref := range p.refs {
		args = append(args, fmt.Sprintf("-providers.kubernetesGateway.gateways[%d].name=%s", i, ref.Name))
		args = append(args, fmt.Sprintf("-providers.kubernetesGateway.gateways[%d].namespace=%s", i, ref.Namespace))
	}
	for _, port := range p.ports {
		args = append(args, fmt.Sprintf("--entryPoints.%s.address=:%d", entryPointName(port), port))
	}

	if proxy == nil {
		return args
	}

	if proxy.Spec.Logs != nil && proxy.Spec.Logs.Level != "" {
		args = append(args, "--log.level="+proxy.Spec.Logs.Level)
	}
	if metricsEnabled(proxy) {
		args = append(args,
			fmt.Sprintf("--entryPoints.metrics.address=:%d", metricsPort),
			"--metrics.prometheus=true",
			"--metrics.prometheus.entryPoint=metrics",
		)
	}
	if proxy.Spec.Tracing != nil && proxy.Spec.Tracing.OTLPEndpoint != "" {
		args = append(args,
			"--tracing.otlp=true",
			"--tracing.otlp.http.endpoint="+proxy.Spec.Tracing.OTLPEndpoint,
		)
	}
	if proxy.Spec.Providers != nil && proxy.Spec.Providers.ThrottleDuration != nil {
		args = append(args, "--providers.providersThrottleDuration="+proxy.Spec.Providers.ThrottleDuration.Duration.String())
	}
	return args
}

func metricsEnabled(proxy *v1alpha1.TraefikProxy) bool {
	return proxy != nil && proxy.Spec.Metrics != nil && proxy.Spec.Metrics.Prometheus
}

// listenerPorts returns the ports of the Gateway listeners and of the listeners
// of its attached ListenerSets, which the data plane serves alongside them.
func listenerPorts(gw *gwapiv1.Gateway, listenerSets []gwapiv1.ListenerSet) []int32 {
	var ports []int32
	for _, l := range gw.Spec.Listeners {
		ports = append(ports, l.Port)
	}
	for _, ls := range listenerSets {
		for _, l := range ls.Spec.Listeners {
			ports = append(ports, l.Port)
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}

func containerPorts(ports []int32, proxy *v1alpha1.TraefikProxy) []corev1.ContainerPort {
	cp := make([]corev1.ContainerPort, 0, len(ports))
	for _, p := range ports {
		cp = append(cp, corev1.ContainerPort{
			Name:          entryPointName(p),
			ContainerPort: p,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	if metricsEnabled(proxy) {
		cp = append(cp, corev1.ContainerPort{
			Name:          "metrics",
			ContainerPort: metricsPort,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	return cp
}

func servicePorts(ports []int32) []corev1.ServicePort {
	sp := make([]corev1.ServicePort, 0, len(ports))
	for _, p := range ports {
		sp = append(sp, corev1.ServicePort{
			Name:       entryPointName(p),
			Port:       p,
			TargetPort: intstr.FromInt32(p),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return sp
}

func entryPointName(port int32) string {
	return fmt.Sprintf("ep-%d", port)
}

func replicas(proxy *v1alpha1.TraefikProxy) *int32 {
	if proxy != nil && proxy.Spec.Deployment != nil && proxy.Spec.Deployment.Replicas != nil {
		return proxy.Spec.Deployment.Replicas
	}
	return ptr.To[int32](1)
}

func image(proxy *v1alpha1.TraefikProxy) string {
	if proxy != nil && proxy.Spec.Deployment != nil && proxy.Spec.Deployment.Image != "" {
		return proxy.Spec.Deployment.Image
	}
	return DefaultImage
}

func resources(proxy *v1alpha1.TraefikProxy) corev1.ResourceRequirements {
	if proxy != nil && proxy.Spec.Deployment != nil {
		return proxy.Spec.Deployment.Resources
	}
	return corev1.ResourceRequirements{}
}

func podLabels(proxy *v1alpha1.TraefikProxy) map[string]string {
	if proxy != nil && proxy.Spec.Deployment != nil {
		return proxy.Spec.Deployment.PodLabels
	}
	return nil
}

func podAnnotations(proxy *v1alpha1.TraefikProxy) map[string]string {
	if proxy != nil && proxy.Spec.Deployment != nil {
		return proxy.Spec.Deployment.PodAnnotations
	}
	return nil
}

func serviceType(proxy *v1alpha1.TraefikProxy) corev1.ServiceType {
	if proxy != nil && proxy.Spec.Service != nil && proxy.Spec.Service.Type != "" {
		return proxy.Spec.Service.Type
	}
	return corev1.ServiceTypeLoadBalancer
}

func loadBalancerClass(proxy *v1alpha1.TraefikProxy) *string {
	if proxy != nil && proxy.Spec.Service != nil && serviceType(proxy) == corev1.ServiceTypeLoadBalancer {
		return proxy.Spec.Service.LoadBalancerClass
	}
	return nil
}

func mergeMaps(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := map[string]string{}
	maps.Copy(out, base)
	maps.Copy(out, extra)
	return out
}
