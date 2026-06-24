// Package status derives Gateway status from the provisioned data plane,
// mirroring Envoy Gateway's UpdateGatewayStatusProgrammedCondition.
package status

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// maxAddresses caps the number of reported addresses, matching the upstream
// behavior of Envoy Gateway.
const maxAddresses = 16

// SupportedAddressType reports whether the operator can honor a Gateway
// spec.address of the given type. Unsupported types make the Gateway
// Accepted=False with reason UnsupportedAddress.
func SupportedAddressType(t *gwapiv1.AddressType) bool {
	if t == nil {
		// A nil type defaults to IPAddress per the Gateway API.
		return true
	}
	switch *t {
	case gwapiv1.IPAddressType, gwapiv1.HostnameAddressType:
		return true
	default:
		return false
	}
}

// AllAddressesSupported reports whether every requested address has a supported
// type.
func AllAddressesSupported(gw *gwapiv1.Gateway) bool {
	for _, a := range gw.Spec.Addresses {
		if !SupportedAddressType(a.Type) {
			return false
		}
	}
	return true
}

// ProgrammedResult is the outcome of deriving addresses and readiness.
type ProgrammedResult struct {
	Addresses  []gwapiv1.GatewayStatusAddress
	Programmed bool
	Reason     gwapiv1.GatewayConditionReason
	Message    string
}

// Derive computes the Gateway status addresses and Programmed outcome from the
// provisioned Service and Deployment. svc or dep may be nil when not yet
// created. nodeAddrs supplies node addresses used for NodePort Services.
func Derive(gw *gwapiv1.Gateway, svc *corev1.Service, dep *appsv1.Deployment, nodeAddrs []string) ProgrammedResult {
	specAddrs := gw.Spec.Addresses

	if svc == nil {
		if len(specAddrs) > 0 {
			return ProgrammedResult{
				Addresses:  fromSpec(specAddrs),
				Programmed: false,
				Reason:     gwapiv1.GatewayReasonAddressNotUsable,
				Message:    "Data plane Service not provisioned yet",
			}
		}
		return ProgrammedResult{
			Programmed: false,
			Reason:     gwapiv1.GatewayReasonPending,
			Message:    "Waiting for data plane Service",
		}
	}

	if len(specAddrs) > 0 {
		return deriveStatic(gw, svc, dep, nodeAddrs)
	}

	addrs := cap16(fromService(svc, nodeAddrs))
	if len(addrs) == 0 {
		return ProgrammedResult{
			Programmed: false,
			Reason:     gwapiv1.GatewayReasonAddressNotAssigned,
			Message:    "No address assigned to the data plane Service",
		}
	}
	if !deploymentReady(dep) {
		return ProgrammedResult{
			Addresses:  addrs,
			Programmed: false,
			Reason:     gwapiv1.GatewayReasonNoResources,
			Message:    "Data plane has no ready replicas",
		}
	}
	return ProgrammedResult{
		Addresses:  addrs,
		Programmed: true,
		Reason:     gwapiv1.GatewayReasonProgrammed,
		Message:    "Data plane provisioned and ready",
	}
}

// deriveStatic handles user requested static addresses. A requested address is
// reported only once the provisioned Service has actually realized it (the
// LoadBalancer assigned it, or it is bound as an external/cluster/node address).
// Any requested address that is not realized leaves the Gateway not Programmed
// with reason AddressNotUsable.
func deriveStatic(gw *gwapiv1.Gateway, svc *corev1.Service, dep *appsv1.Deployment, nodeAddrs []string) ProgrammedResult {
	realized := realizedSet(svc, nodeAddrs)

	var bound []gwapiv1.GatewayStatusAddress
	allUsable := true
	for _, a := range gw.Spec.Addresses {
		t := gwapiv1.IPAddressType
		if a.Type != nil {
			t = *a.Type
		}
		if realized[a.Value] {
			bound = append(bound, addr(t, a.Value))
		} else {
			allUsable = false
		}
	}
	bound = cap16(bound)

	if len(bound) == 0 || !allUsable {
		return ProgrammedResult{
			Addresses:  bound,
			Programmed: false,
			Reason:     gwapiv1.GatewayReasonAddressNotUsable,
			Message:    "One or more requested addresses are not usable by the data plane",
		}
	}
	if !deploymentReady(dep) {
		return ProgrammedResult{
			Addresses:  bound,
			Programmed: false,
			Reason:     gwapiv1.GatewayReasonNoResources,
			Message:    "Data plane has no ready replicas",
		}
	}
	return ProgrammedResult{
		Addresses:  bound,
		Programmed: true,
		Reason:     gwapiv1.GatewayReasonProgrammed,
		Message:    "Data plane provisioned with the requested static addresses",
	}
}

// realizedSet is the set of address values the Service has actually bound. For
// a LoadBalancer this is the assigned ingress (the cloud grants usable
// addresses and rejects unusable ones); for ClusterIP/NodePort it is the
// external, cluster and node addresses.
func realizedSet(svc *corev1.Service, nodeAddrs []string) map[string]bool {
	out := map[string]bool{}
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				out[ing.IP] = true
			}
			if ing.Hostname != "" {
				out[ing.Hostname] = true
			}
		}
	default:
		for _, ip := range svc.Spec.ExternalIPs {
			out[ip] = true
		}
		for _, a := range fromClusterIPs(svc) {
			out[a.Value] = true
		}
		if svc.Spec.Type == corev1.ServiceTypeNodePort {
			for _, n := range nodeAddrs {
				out[n] = true
			}
		}
	}
	return out
}

func fromService(svc *corev1.Service, nodeAddrs []string) []gwapiv1.GatewayStatusAddress {
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		var out []gwapiv1.GatewayStatusAddress
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				out = append(out, addr(gwapiv1.IPAddressType, ing.IP))
			} else if ing.Hostname != "" {
				out = append(out, addr(gwapiv1.HostnameAddressType, ing.Hostname))
			}
		}
		return out
	case corev1.ServiceTypeNodePort:
		var out []gwapiv1.GatewayStatusAddress
		for _, n := range nodeAddrs {
			out = append(out, addr(gwapiv1.IPAddressType, n))
		}
		return out
	case corev1.ServiceTypeClusterIP:
		return fromClusterIPs(svc)
	default:
		return nil
	}
}

func fromClusterIPs(svc *corev1.Service) []gwapiv1.GatewayStatusAddress {
	var out []gwapiv1.GatewayStatusAddress
	for _, ip := range svc.Spec.ClusterIPs {
		if ip == "" || ip == corev1.ClusterIPNone {
			continue
		}
		out = append(out, addr(gwapiv1.IPAddressType, ip))
	}
	if len(out) == 0 && svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != corev1.ClusterIPNone {
		out = append(out, addr(gwapiv1.IPAddressType, svc.Spec.ClusterIP))
	}
	return out
}

func fromSpec(specAddrs []gwapiv1.GatewaySpecAddress) []gwapiv1.GatewayStatusAddress {
	var out []gwapiv1.GatewayStatusAddress
	for _, a := range specAddrs {
		t := gwapiv1.IPAddressType
		if a.Type != nil {
			t = *a.Type
		}
		out = append(out, addr(t, a.Value))
	}
	return out
}

func deploymentReady(dep *appsv1.Deployment) bool {
	return dep != nil && dep.Status.ReadyReplicas > 0
}

func addr(t gwapiv1.AddressType, value string) gwapiv1.GatewayStatusAddress {
	return gwapiv1.GatewayStatusAddress{Type: ptr.To(t), Value: value}
}

func cap16(addrs []gwapiv1.GatewayStatusAddress) []gwapiv1.GatewayStatusAddress {
	if len(addrs) > maxAddresses {
		return addrs[:maxAddresses]
	}
	return addrs
}
