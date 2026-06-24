package provisioner

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
)

// Target is the resolved deployment topology for a Gateway data plane.
type Target struct {
	Mode      v1alpha1.DeploymentMode
	Namespace string

	// Merged is true when the data plane is shared by every Gateway of the
	// GatewayClass. MergeKey is then the GatewayClass name.
	Merged   bool
	MergeKey string
}

// Dedicated reports whether the data plane is provisioned in a shared
// namespace rather than the Gateway's own namespace. Merged data planes are
// always provisioned this way.
func (t Target) Dedicated() bool {
	return t.Merged || t.Mode == v1alpha1.DedicatedMode
}

// Resolve computes the deployment mode and target namespace for a Gateway from
// the resolved TraefikProxy and the operator default namespace. The default
// mode is GatewayNamespace, which provisions in the Gateway's own namespace.
// When the TraefikProxy enables mergeGateways, the data plane is shared by the
// whole GatewayClass and provisioned in a single namespace.
func Resolve(gw *gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy, defaultNamespace string) Target {
	mode := v1alpha1.GatewayNamespaceMode
	namespace := gw.Namespace

	if proxy != nil && proxy.Spec.MergeGateways {
		ns := defaultNamespace
		if proxy.Spec.Provisioning != nil && proxy.Spec.Provisioning.Namespace != "" {
			ns = proxy.Spec.Provisioning.Namespace
		}
		return Target{
			Mode:      v1alpha1.DedicatedMode,
			Namespace: ns,
			Merged:    true,
			MergeKey:  string(gw.Spec.GatewayClassName),
		}
	}

	if proxy != nil && proxy.Spec.Provisioning != nil {
		if proxy.Spec.Provisioning.Mode != "" {
			mode = proxy.Spec.Provisioning.Mode
		}
		if mode == v1alpha1.DedicatedMode {
			namespace = proxy.Spec.Provisioning.Namespace
			if namespace == "" {
				namespace = defaultNamespace
			}
		}
	}

	return Target{Mode: mode, Namespace: namespace}
}
