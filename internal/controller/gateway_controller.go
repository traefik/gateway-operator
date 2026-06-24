package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
	"github.com/traefik/traefik-operator/internal/provisioner"
	"github.com/traefik/traefik-operator/internal/status"
)

const (
	// dataPlaneFinalizer guards Dedicated-mode data planes, whose resources
	// live in another namespace and so cannot be garbage collected through
	// owner references.
	dataPlaneFinalizer = "gateway.traefik.io/data-plane"

	// provisionedNamespaceAnnotation records where the data plane was
	// provisioned, so cleanup is reliable even if the TraefikProxy is gone.
	provisionedNamespaceAnnotation = "gateway.traefik.io/provisioned-namespace"
)

// GatewayReconciler provisions a data plane per Gateway and owns its status.
type GatewayReconciler struct {
	client.Client

	// DataPlaneNamespace is the default namespace for Dedicated-mode data
	// planes when the TraefikProxy does not set one.
	DataPlaneNamespace string
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.traefik.io,resources=traefikproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile provisions the data plane for a Gateway and writes its status.
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gw gwapiv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	owned, err := r.ownsGateway(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !owned {
		// A Gateway we no longer manage (class changed) may still carry our
		// finalizer and orphaned Dedicated-mode resources. Clean up.
		if controllerutil.ContainsFinalizer(&gw, dataPlaneFinalizer) {
			return ctrl.Result{}, r.finalize(ctx, &gw)
		}
		return ctrl.Result{}, nil
	}

	proxy, err := r.resolveProxy(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}
	target := provisioner.Resolve(&gw, proxy, r.DataPlaneNamespace)

	// Handle deletion: tear down Dedicated-mode resources, then release the
	// finalizer. Gateway-namespace resources are garbage collected by owner
	// references and need no finalizer.
	if !gw.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&gw, dataPlaneFinalizer) {
			return ctrl.Result{}, r.finalize(ctx, &gw)
		}
		return ctrl.Result{}, nil
	}

	// Reject Gateways requesting an address type the operator cannot honor,
	// without provisioning any data plane for them.
	if !status.AllAddressesSupported(&gw) {
		r.writeUnsupportedAddress(&gw)
		if err := r.Status().Update(ctx, &gw); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Maintain the finalizer and provisioned-namespace marker for Dedicated
	// mode; remove them when running in Gateway-namespace mode.
	if changed := r.reconcileFinalizer(&gw, target); changed {
		if err := r.Update(ctx, &gw); err != nil {
			return ctrl.Result{}, err
		}
	}

	set := provisioner.Render(&gw, proxy, target.Namespace)
	if err := r.applySet(ctx, &gw, set, target); err != nil {
		return ctrl.Result{}, err
	}

	svc, dep, err := r.observe(ctx, set, target.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	nodeAddrs, err := r.nodeAddresses(ctx, svc)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.writeStatus(&gw, svc, dep, nodeAddrs)
	if err := r.Status().Update(ctx, &gw); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileFinalizer adds the finalizer and provisioned-namespace annotation in
// Dedicated mode, and removes them otherwise. It reports whether the Gateway
// metadata changed and needs an update.
func (r *GatewayReconciler) reconcileFinalizer(gw *gwapiv1.Gateway, target provisioner.Target) bool {
	if !target.Dedicated() {
		removed := controllerutil.RemoveFinalizer(gw, dataPlaneFinalizer)
		if gw.Annotations[provisionedNamespaceAnnotation] != "" {
			delete(gw.Annotations, provisionedNamespaceAnnotation)
			removed = true
		}
		return removed
	}

	changed := controllerutil.AddFinalizer(gw, dataPlaneFinalizer)
	if gw.Annotations[provisionedNamespaceAnnotation] != target.Namespace {
		if gw.Annotations == nil {
			gw.Annotations = map[string]string{}
		}
		gw.Annotations[provisionedNamespaceAnnotation] = target.Namespace
		changed = true
	}
	return changed
}

// finalize deletes the Dedicated-mode data plane resources and removes the
// finalizer.
func (r *GatewayReconciler) finalize(ctx context.Context, gw *gwapiv1.Gateway) error {
	namespace := gw.Annotations[provisionedNamespaceAnnotation]
	if namespace == "" {
		namespace = r.DataPlaneNamespace
	}
	if err := r.cleanup(ctx, gw, namespace); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(gw, dataPlaneFinalizer)
	delete(gw.Annotations, provisionedNamespaceAnnotation)
	return r.Update(ctx, gw)
}

// cleanup deletes the data plane resources provisioned for a Gateway in the
// given namespace. The resource set is a single named object of each kind.
func (r *GatewayReconciler) cleanup(ctx context.Context, gw *gwapiv1.Gateway, namespace string) error {
	name := provisioner.ResourceName(gw)
	om := metav1.ObjectMeta{Name: name, Namespace: namespace}
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: om},
		&corev1.Service{ObjectMeta: om},
		&corev1.ServiceAccount{ObjectMeta: om},
	} {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// ownsGateway reports whether the Gateway references a GatewayClass handled by
// the operator.
func (r *GatewayReconciler) ownsGateway(ctx context.Context, gw *gwapiv1.Gateway) (bool, error) {
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, types.NamespacedName{Name: string(gw.Spec.GatewayClassName)}, &gc); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return gc.Spec.ControllerName == provisioner.ControllerName, nil
}

// resolveProxy resolves the TraefikProxy referenced by the Gateway's
// GatewayClass parametersRef. A missing reference yields nil (defaults apply).
func (r *GatewayReconciler) resolveProxy(ctx context.Context, gw *gwapiv1.Gateway) (*v1alpha1.TraefikProxy, error) {
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, types.NamespacedName{Name: string(gw.Spec.GatewayClassName)}, &gc); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	ref := gc.Spec.ParametersRef
	if ref == nil || string(ref.Group) != v1alpha1.GroupVersion.Group || string(ref.Kind) != "TraefikProxy" {
		return nil, nil
	}

	var proxy v1alpha1.TraefikProxy
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name}, &proxy); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &proxy, nil
}

func (r *GatewayReconciler) applySet(ctx context.Context, gw *gwapiv1.Gateway, set *provisioner.ResourceSet, target provisioner.Target) error {
	// Owner references only work within the Gateway namespace. In Dedicated
	// mode the data plane lives elsewhere and is reclaimed by the finalizer.
	owner := !target.Dedicated()

	if err := r.apply(ctx, gw, set.ServiceAccount, owner, func() {}); err != nil {
		return fmt.Errorf("serviceaccount: %w", err)
	}

	desiredDep := set.Deployment.DeepCopy()
	if err := r.apply(ctx, gw, set.Deployment, owner, func() {
		set.Deployment.Spec = desiredDep.Spec
		set.Deployment.Labels = desiredDep.Labels
		set.Deployment.Annotations = desiredDep.Annotations
	}); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}

	desiredSvc := set.Service.DeepCopy()
	if err := r.apply(ctx, gw, set.Service, owner, func() {
		// Preserve API-server assigned fields (ClusterIP is immutable).
		set.Service.Spec.Type = desiredSvc.Spec.Type
		set.Service.Spec.Selector = desiredSvc.Spec.Selector
		set.Service.Spec.Ports = desiredSvc.Spec.Ports
		set.Service.Spec.LoadBalancerClass = desiredSvc.Spec.LoadBalancerClass
		set.Service.Spec.ExternalIPs = desiredSvc.Spec.ExternalIPs
		set.Service.Spec.LoadBalancerIP = desiredSvc.Spec.LoadBalancerIP
		set.Service.Labels = desiredSvc.Labels
		set.Service.Annotations = desiredSvc.Annotations
	}); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

// apply creates or updates obj. When owner is true the Gateway is set as
// controller owner so Kubernetes garbage collects the resource on Gateway
// deletion (Gateway-namespace mode).
func (r *GatewayReconciler) apply(ctx context.Context, gw *gwapiv1.Gateway, obj client.Object, owner bool, mutate func()) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		mutate()
		if owner {
			return controllerutil.SetControllerReference(gw, obj, r.Scheme())
		}
		return nil
	})
	return err
}

func (r *GatewayReconciler) observe(ctx context.Context, set *provisioner.ResourceSet, namespace string) (*corev1.Service, *appsv1.Deployment, error) {
	var svc corev1.Service
	svcPtr := &svc
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: set.Service.Name}, &svc); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		svcPtr = nil
	}

	var dep appsv1.Deployment
	depPtr := &dep
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: set.Deployment.Name}, &dep); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		depPtr = nil
	}
	return svcPtr, depPtr, nil
}

// nodeAddresses lists node addresses used to populate the status of a NodePort
// Service. It returns nil for other Service types.
func (r *GatewayReconciler) nodeAddresses(ctx context.Context, svc *corev1.Service) ([]string, error) {
	if svc == nil || svc.Spec.Type != corev1.ServiceTypeNodePort {
		return nil, nil
	}
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return nil, err
	}
	var addrs []string
	for i := range nodes.Items {
		var internal string
		for _, a := range nodes.Items[i].Status.Addresses {
			switch a.Type {
			case corev1.NodeExternalIP:
				addrs = append(addrs, a.Address)
			case corev1.NodeInternalIP:
				internal = a.Address
			}
		}
		// Fall back to the internal IP when no external IP is advertised.
		if internal != "" && !contains(addrs, internal) {
			addrs = append(addrs, internal)
		}
	}
	return addrs, nil
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// writeUnsupportedAddress marks a Gateway not accepted because it requests an
// address type the operator does not support.
func (r *GatewayReconciler) writeUnsupportedAddress(gw *gwapiv1.Gateway) {
	gw.Status.Addresses = nil
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               string(gwapiv1.GatewayConditionAccepted),
		Status:             metav1.ConditionFalse,
		Reason:             string(gwapiv1.GatewayReasonUnsupportedAddress),
		Message:            "Gateway requests an unsupported address type",
		ObservedGeneration: gw.Generation,
	})
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               string(gwapiv1.GatewayConditionProgrammed),
		Status:             metav1.ConditionFalse,
		Reason:             string(gwapiv1.GatewayReasonInvalid),
		Message:            "Gateway not accepted",
		ObservedGeneration: gw.Generation,
	})
	gw.Status.Listeners = r.listenerStatus(gw, metav1.ConditionFalse)
}

func (r *GatewayReconciler) writeStatus(gw *gwapiv1.Gateway, svc *corev1.Service, dep *appsv1.Deployment, nodeAddrs []string) {
	res := status.Derive(gw, svc, dep, nodeAddrs)
	gw.Status.Addresses = res.Addresses

	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               string(gwapiv1.GatewayConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gwapiv1.GatewayReasonAccepted),
		Message:            "Gateway accepted by traefik-operator",
		ObservedGeneration: gw.Generation,
	})

	progStatus := metav1.ConditionFalse
	if res.Programmed {
		progStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               string(gwapiv1.GatewayConditionProgrammed),
		Status:             progStatus,
		Reason:             string(res.Reason),
		Message:            res.Message,
		ObservedGeneration: gw.Generation,
	})

	gw.Status.Listeners = r.listenerStatus(gw, progStatus)
}

// listenerStatus reports a minimal per-listener status. Full route attachment
// accounting is delegated to the data plane provider in a later phase.
func (r *GatewayReconciler) listenerStatus(gw *gwapiv1.Gateway, prog metav1.ConditionStatus) []gwapiv1.ListenerStatus {
	out := make([]gwapiv1.ListenerStatus, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		ls := gwapiv1.ListenerStatus{
			Name:           l.Name,
			SupportedKinds: supportedKinds(l.Protocol),
			AttachedRoutes: 0,
		}
		set := func(t string, s metav1.ConditionStatus, reason string) {
			meta.SetStatusCondition(&ls.Conditions, metav1.Condition{
				Type:               t,
				Status:             s,
				Reason:             reason,
				ObservedGeneration: gw.Generation,
			})
		}
		set(string(gwapiv1.ListenerConditionAccepted), metav1.ConditionTrue, string(gwapiv1.ListenerReasonAccepted))
		set(string(gwapiv1.ListenerConditionResolvedRefs), metav1.ConditionTrue, string(gwapiv1.ListenerReasonResolvedRefs))
		progReason := string(gwapiv1.ListenerReasonProgrammed)
		if prog != metav1.ConditionTrue {
			progReason = string(gwapiv1.ListenerReasonPending)
		}
		set(string(gwapiv1.ListenerConditionProgrammed), prog, progReason)
		out = append(out, ls)
	}
	return out
}

func supportedKinds(proto gwapiv1.ProtocolType) []gwapiv1.RouteGroupKind {
	group := gwapiv1.Group(gwapiv1.GroupName)
	switch proto {
	case gwapiv1.HTTPProtocolType, gwapiv1.HTTPSProtocolType:
		return []gwapiv1.RouteGroupKind{{Group: &group, Kind: "HTTPRoute"}}
	case gwapiv1.TLSProtocolType:
		return []gwapiv1.RouteGroupKind{{Group: &group, Kind: "TLSRoute"}}
	case gwapiv1.TCPProtocolType:
		return []gwapiv1.RouteGroupKind{{Group: &group, Kind: "TCPRoute"}}
	default:
		return nil
	}
}

// SetupWithManager registers the reconciler and its owned resources.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.Gateway{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Complete(r)
}
