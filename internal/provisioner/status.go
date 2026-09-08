package provisioner

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// StaticAddressRejection reports the conditions that reject a Gateway whose
// spec.addresses request an unsupported address type, or nil when every
// requested type is supported. An unsupported type is a hard spec error decided
// from the Gateway alone: the requested configuration can never be satisfied,
// so the operator uses it to keep the data plane from being provisioned and to
// publish the Gateway status itself. The returned conditions carry a zero
// LastTransitionTime for the caller to stamp.
func StaticAddressRejection(gw *gwapiv1.Gateway) []metav1.Condition {
	for _, a := range gw.Spec.Addresses {
		if supportedAddressType(a.Type) {
			continue
		}
		return []metav1.Condition{
			{
				Type:               string(gwapiv1.GatewayConditionAccepted),
				Status:             metav1.ConditionFalse,
				ObservedGeneration: gw.Generation,
				Reason:             string(gwapiv1.GatewayReasonUnsupportedAddress),
				Message:            "Gateway requests an unsupported address type",
			},
			{
				Type:               string(gwapiv1.GatewayConditionProgrammed),
				Status:             metav1.ConditionFalse,
				ObservedGeneration: gw.Generation,
				Reason:             string(gwapiv1.GatewayReasonInvalid),
				Message:            "Gateway not accepted",
			},
		}
	}
	return nil
}

// RealizedAddresses reports the addresses the provisioned Service has actually
// been assigned, read from its load balancer ingress. A requested static
// address counts as realized only once it surfaces here.
func RealizedAddresses(svc *corev1.Service) []gwapiv1.GatewayStatusAddress {
	if svc == nil {
		return nil
	}
	var addrs []gwapiv1.GatewayStatusAddress
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		switch {
		case ing.IP != "":
			addrs = append(addrs, gwapiv1.GatewayStatusAddress{
				Type:  ptr.To(gwapiv1.IPAddressType),
				Value: ing.IP,
			})
		case ing.Hostname != "":
			addrs = append(addrs, gwapiv1.GatewayStatusAddress{
				Type:  ptr.To(gwapiv1.HostnameAddressType),
				Value: ing.Hostname,
			})
		}
	}
	return addrs
}

// StaticAddressStatus reconciles the supported static addresses a Gateway
// requests against the addresses the provisioned Service realized. It reports
// the addresses the operator should publish, the conditions it should write,
// and whether the data plane may be provisioned.
//
//   - No requested addresses: the data plane runs and owns the Gateway status
//     (including addresses, from its Service), so the operator publishes nothing.
//   - Every requested address realized: same, the data plane owns the status.
//   - A requested address not yet realized: the address can never be honored by
//     a running data plane, so none is provisioned; the operator publishes
//     Accepted=True with Programmed=False/AddressNotUsable and the realized
//     subset of the requested addresses.
//
// Unsupported address types are handled by StaticAddressRejection before this.
// The returned conditions carry a zero LastTransitionTime for the caller to
// stamp.
func StaticAddressStatus(gw *gwapiv1.Gateway, realized []gwapiv1.GatewayStatusAddress) (addrs []gwapiv1.GatewayStatusAddress, conds []metav1.Condition, provision bool) {
	specAddrs := gw.Spec.Addresses
	if len(specAddrs) == 0 {
		return nil, nil, true
	}

	realizedSet := make(map[string]struct{}, len(realized))
	for _, a := range realized {
		realizedSet[a.Value] = struct{}{}
	}

	var matched []gwapiv1.GatewayStatusAddress
	allUsable := true
	for _, a := range specAddrs {
		if _, ok := realizedSet[a.Value]; !ok {
			allUsable = false
			continue
		}
		t := gwapiv1.IPAddressType
		if a.Type != nil {
			t = *a.Type
		}
		matched = append(matched, gwapiv1.GatewayStatusAddress{Type: ptr.To(t), Value: a.Value})
	}

	if allUsable {
		return nil, nil, true
	}

	return matched, []metav1.Condition{
		{
			Type:               string(gwapiv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gw.Generation,
			Reason:             string(gwapiv1.GatewayReasonAccepted),
			Message:            "Gateway successfully scheduled",
		},
		{
			Type:               string(gwapiv1.GatewayConditionProgrammed),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: gw.Generation,
			Reason:             string(gwapiv1.GatewayReasonAddressNotUsable),
			Message:            "One or more requested addresses are not usable",
		},
	}, false
}

// supportedAddressType reports whether the operator can honor a Gateway
// spec.address of the given type. A nil type defaults to IPAddress per the
// Gateway API.
func supportedAddressType(t *gwapiv1.AddressType) bool {
	if t == nil {
		return true
	}
	switch *t {
	case gwapiv1.IPAddressType, gwapiv1.HostnameAddressType:
		return true
	default:
		return false
	}
}
