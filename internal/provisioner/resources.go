package provisioner

import (
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
)

// ResourceSet is the data plane resource set provisioned for one Gateway.
type ResourceSet struct {
	ServiceAccount *corev1.ServiceAccount
	Deployment     *appsv1.Deployment
	Service        *corev1.Service
}

// Render builds the desired data plane resource set for a Gateway, applying the
// resolved TraefikProxy configuration. Resources are rendered in the Gateway
// namespace (gateway-namespace mode). Owner references are set by the caller.
func Render(gw *gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy) *ResourceSet {
	name := ResourceName(gw)
	ports := listenerPorts(gw)

	sa := &corev1.ServiceAccount{
		ObjectMeta: objectMeta(gw, name),
	}

	dep := &appsv1.Deployment{
		ObjectMeta: objectMeta(gw, name),
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas(proxy),
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels(gw)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      mergeMaps(selectorLabels(gw), podLabels(proxy)),
					Annotations: podAnnotations(proxy),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: name,
					Containers: []corev1.Container{{
						Name:      "traefik",
						Image:     image(proxy),
						Args:      containerArgs(gw, ports),
						Ports:     containerPorts(ports),
						Resources: resources(proxy),
					}},
				},
			},
		},
	}

	svc := &corev1.Service{
		ObjectMeta: serviceObjectMeta(gw, name, proxy),
		Spec: corev1.ServiceSpec{
			Type:              serviceType(proxy),
			Selector:          selectorLabels(gw),
			Ports:             servicePorts(ports),
			LoadBalancerClass: loadBalancerClass(proxy),
		},
	}

	return &ResourceSet{ServiceAccount: sa, Deployment: dep, Service: svc}
}

func objectMeta(gw *gwapiv1.Gateway, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        name,
		Namespace:   gw.Namespace,
		Labels:      commonLabels(gw),
		Annotations: infraAnnotations(gw),
	}
}

func serviceObjectMeta(gw *gwapiv1.Gateway, name string, proxy *v1alpha1.TraefikProxy) metav1.ObjectMeta {
	m := objectMeta(gw, name)
	if proxy != nil && proxy.Spec.Service != nil {
		m.Annotations = mergeMaps(m.Annotations, proxy.Spec.Service.Annotations)
	}
	return m
}

// commonLabels carries the operator markers plus the propagated
// spec.infrastructure.labels (GatewayInfrastructurePropagation).
func commonLabels(gw *gwapiv1.Gateway) map[string]string {
	l := map[string]string{
		GatewayNameLabel: gw.Name,
		ManagedByLabel:   ManagedByValue,
	}
	if gw.Spec.Infrastructure != nil {
		for k, v := range gw.Spec.Infrastructure.Labels {
			l[string(k)] = string(v)
		}
	}
	return l
}

// selectorLabels identify the pods of a Gateway data plane. They must be stable
// and must not include propagated infrastructure labels (those can change).
func selectorLabels(gw *gwapiv1.Gateway) map[string]string {
	return map[string]string{
		GatewayNameLabel: gw.Name,
		ManagedByLabel:   ManagedByValue,
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
// provider is scoped to the single Gateway and told not to write Gateway status
// (the operator is the single writer). Both scoping flags are proposed Traefik
// additions, see DESIGN.md section 8.
func containerArgs(gw *gwapiv1.Gateway, ports []int32) []string {
	args := []string{
		"--providers.kubernetesGateway=true",
		fmt.Sprintf("--providers.kubernetesGateway.gateway=%s/%s", gw.Namespace, gw.Name),
		"--providers.kubernetesGateway.statusManagement=false",
	}
	for _, p := range ports {
		args = append(args, fmt.Sprintf("--entryPoints.%s.address=:%d", entryPointName(p), p))
	}
	return args
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

func containerPorts(ports []int32) []corev1.ContainerPort {
	cp := make([]corev1.ContainerPort, 0, len(ports))
	for _, p := range ports {
		cp = append(cp, corev1.ContainerPort{
			Name:          entryPointName(p),
			ContainerPort: p,
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
