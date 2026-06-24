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

// ProgrammedResult is the outcome of deriving addresses and readiness.
type ProgrammedResult struct {
	Addresses  []gwapiv1.GatewayStatusAddress
	Programmed bool
	Reason     gwapiv1.GatewayConditionReason
	Message    string
}

// Derive computes the Gateway status addresses and Programmed outcome from the
// provisioned Service and Deployment. svc or dep may be nil when not yet created.
func Derive(gw *gwapiv1.Gateway, svc *corev1.Service, dep *appsv1.Deployment) ProgrammedResult {
	specAddrs := gw.Spec.Addresses

	// Service not created yet but the user requested addresses: surface them and
	// mark the Gateway not usable.
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

	var addrs []gwapiv1.GatewayStatusAddress
	if len(specAddrs) > 0 {
		// Static addresses requested: report the externalIPs / clusterIPs the
		// Service is bound to.
		addrs = fromStaticService(svc, specAddrs)
	} else {
		addrs = fromService(svc)
	}
	addrs = cap16(addrs)

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

func fromService(svc *corev1.Service) []gwapiv1.GatewayStatusAddress {
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
	case corev1.ServiceTypeClusterIP, corev1.ServiceTypeNodePort:
		return fromClusterIPs(svc)
	default:
		return nil
	}
}

func fromStaticService(svc *corev1.Service, specAddrs []gwapiv1.GatewaySpecAddress) []gwapiv1.GatewayStatusAddress {
	var out []gwapiv1.GatewayStatusAddress
	for _, ip := range svc.Spec.ExternalIPs {
		out = append(out, addr(gwapiv1.IPAddressType, ip))
	}
	if len(out) == 0 {
		out = fromClusterIPs(svc)
	}
	if len(out) == 0 {
		// Nothing bound yet: echo the requested addresses.
		out = fromSpec(specAddrs)
	}
	return out
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
