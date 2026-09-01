package provisioner

import (
	"fmt"
	"sort"

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

	"github.com/traefik/operator/api/v1alpha1"
)

// metricsPort is the container port exposing Prometheus metrics.
const metricsPort int32 = 9100

// DataPlaneClusterRole is the name of the static ClusterRole the operator binds
// to each provisioned data plane ServiceAccount. It carries the read and status
// permissions the in-tree kubernetesGateway provider requires, and is shipped
// with the operator install (config/rbac/dataplane_role.yaml).
const DataPlaneClusterRole = "traefik-gateway-dataplane"

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

// buildParams describes a data plane resource set to render. It is shared by
// the single Gateway and merged Gateway rendering paths.
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

// Render builds the desired data plane resource set for a single Gateway,
// applying the resolved TraefikProxy configuration. Resources are rendered in
// namespace. Owner references are set by the caller.
func Render(gw *gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy, namespace string) *ResourceSet {
	set := build(buildParams{
		name:        ResourceName(gw),
		namespace:   namespace,
		selector:    SelectorLabels(gw),
		labels:      commonLabels(gw),
		annotations: infraAnnotations(gw),
		ports:       listenerPorts(gw),
		refs:        []types.NamespacedName{{Namespace: gw.Namespace, Name: gw.Name}},
		proxy:       proxy,
	})
	if proxy != nil && proxy.Spec.Service != nil {
		set.Service.Annotations = mergeMaps(set.Service.Annotations, proxy.Spec.Service.Annotations)
	}
	applyStaticAddresses(set.Service, gw)
	return set
}

// RenderMerged builds the single data plane resource set shared by all Gateways
// of a merged GatewayClass. Listener ports are the union across the merged
// Gateways and the data plane is scoped to all of them.
func RenderMerged(class string, gateways []*gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy, namespace string) *ResourceSet {
	selector := map[string]string{
		MergeGroupLabel: class,
		ManagedByLabel:  ManagedByValue,
	}
	return build(buildParams{
		name:      MergedResourceName(class),
		namespace: namespace,
		selector:  selector,
		labels:    selector,
		ports:     mergedPorts(gateways),
		refs:      gatewayRefs(gateways),
		proxy:     proxy,
	})
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

func mergedPorts(gateways []*gwapiv1.Gateway) []int32 {
	seen := map[int32]struct{}{}
	var ports []int32
	for _, gw := range gateways {
		for _, p := range listenerPorts(gw) {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			ports = append(ports, p)
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}

func gatewayRefs(gateways []*gwapiv1.Gateway) []types.NamespacedName {
	refs := make([]types.NamespacedName, 0, len(gateways))
	for _, gw := range gateways {
		refs = append(refs, types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].String() < refs[j].String()
	})
	return refs
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

// SelectorLabels identify the data plane resources of a Gateway. They are
// stable and must not include propagated infrastructure labels (those can
// change). The namespace label keeps them unambiguous when several Gateways
// share one namespace in Dedicated mode.
func SelectorLabels(gw *gwapiv1.Gateway) map[string]string {
	return map[string]string{
		GatewayNameLabel:      gw.Name,
		GatewayNamespaceLabel: gw.Namespace,
		ManagedByLabel:        ManagedByValue,
	}
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
// provider is scoped to the data plane's Gateway(s) and reports the address of
// the Service the operator provisioned for it. GatewayClass status is left to
// the operator (the single GatewayClass writer); the data plane writes its own
// Gateway, listener and route status. See DESIGN.md section 8.
func containerArgs(p buildParams) []string {
	proxy := p.proxy
	args := []string{
		"--providers.kubernetesGateway=true",
		"--providers.kubernetesGateway.disableGatewayClassStatus=true",
		"--providers.kubernetesGateway.experimentalChannel=true",
		fmt.Sprintf("--providers.kubernetesGateway.statusAddress.service.name=%s", p.name),
		fmt.Sprintf("--providers.kubernetesGateway.statusAddress.service.namespace=%s", p.namespace),
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
		args = append(args, fmt.Sprintf("--log.level=%s", proxy.Spec.Logs.Level))
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
			fmt.Sprintf("--tracing.otlp.http.endpoint=%s", proxy.Spec.Tracing.OTLPEndpoint),
		)
	}
	return args
}

func metricsEnabled(proxy *v1alpha1.TraefikProxy) bool {
	return proxy != nil && proxy.Spec.Metrics != nil && proxy.Spec.Metrics.Prometheus
}

func listenerPorts(gw *gwapiv1.Gateway) []int32 {
	seen := map[int32]struct{}{}
	var ports []int32
	for _, l := range gw.Spec.Listeners {
		p := int32(l.Port)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
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
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
