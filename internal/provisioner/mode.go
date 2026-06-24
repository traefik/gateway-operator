package provisioner

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
)

// Target is the resolved deployment topology for a Gateway data plane.
type Target struct {
	Mode      v1alpha1.DeploymentMode
	Namespace string
}

// Dedicated reports whether the data plane is provisioned in a shared
// namespace rather than the Gateway's own namespace.
func (t Target) Dedicated() bool {
	return t.Mode == v1alpha1.DedicatedMode
}

// Resolve computes the deployment mode and target namespace for a Gateway from
// the resolved TraefikProxy and the operator default namespace. The default
// mode is GatewayNamespace, which provisions in the Gateway's own namespace.
func Resolve(gw *gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy, defaultNamespace string) Target {
	mode := v1alpha1.GatewayNamespaceMode
	namespace := gw.Namespace

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
