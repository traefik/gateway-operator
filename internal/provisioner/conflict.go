package provisioner

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ListenerRef identifies a listener of a specific Gateway.
type ListenerRef struct {
	GatewayNamespace string
	GatewayName      string
	Listener         gwapiv1.SectionName
}

// DetectListenerConflicts finds listeners that conflict across a merged group
// of Gateways. Listeners on the same port with differing protocols conflict
// (ProtocolConflict); listeners on the same port and protocol with the same
// hostname conflict (HostnameConflict). The returned map holds only the
// conflicting listeners.
func DetectListenerConflicts(gateways []*gwapiv1.Gateway) map[ListenerRef]gwapiv1.ListenerConditionReason {
	type entry struct {
		ref   ListenerRef
		proto gwapiv1.ProtocolType
		host  string
	}

	byPort := map[gwapiv1.PortNumber][]entry{}
	for _, gw := range gateways {
		for _, l := range gw.Spec.Listeners {
			host := ""
			if l.Hostname != nil {
				host = string(*l.Hostname)
			}
			byPort[l.Port] = append(byPort[l.Port], entry{
				ref:   ListenerRef{GatewayNamespace: gw.Namespace, GatewayName: gw.Name, Listener: l.Name},
				proto: l.Protocol,
				host:  host,
			})
		}
	}

	conflicts := map[ListenerRef]gwapiv1.ListenerConditionReason{}
	for _, entries := range byPort {
		protos := map[gwapiv1.ProtocolType]struct{}{}
		for _, e := range entries {
			protos[e.proto] = struct{}{}
		}
		if len(protos) > 1 {
			for _, e := range entries {
				conflicts[e.ref] = gwapiv1.ListenerReasonProtocolConflict
			}
			continue
		}

		byHost := map[string][]ListenerRef{}
		for _, e := range entries {
			byHost[e.host] = append(byHost[e.host], e.ref)
		}
		for _, refs := range byHost {
			if len(refs) > 1 {
				for _, ref := range refs {
					conflicts[ref] = gwapiv1.ListenerReasonHostnameConflict
				}
			}
		}
	}
	return conflicts
}
