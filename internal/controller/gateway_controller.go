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

// GatewayReconciler provisions a data plane per Gateway and owns its status.
type GatewayReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=update;patch
// +kubebuilder:rbac:groups=gateway.traefik.io,resources=traefikproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete

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
		return ctrl.Result{}, nil
	}

	proxy, err := r.resolveProxy(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}

	set := provisioner.Render(&gw, proxy)
	if err := r.applySet(ctx, &gw, set); err != nil {
		return ctrl.Result{}, err
	}

	svc, dep, err := r.observe(ctx, &gw, set)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.writeStatus(&gw, svc, dep)
	if err := r.Status().Update(ctx, &gw); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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

func (r *GatewayReconciler) applySet(ctx context.Context, gw *gwapiv1.Gateway, set *provisioner.ResourceSet) error {
	if err := r.apply(ctx, gw, set.ServiceAccount, func() {}); err != nil {
		return fmt.Errorf("serviceaccount: %w", err)
	}

	desiredDep := set.Deployment.DeepCopy()
	if err := r.apply(ctx, gw, set.Deployment, func() {
		set.Deployment.Spec = desiredDep.Spec
		set.Deployment.Labels = desiredDep.Labels
		set.Deployment.Annotations = desiredDep.Annotations
	}); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}

	desiredSvc := set.Service.DeepCopy()
	if err := r.apply(ctx, gw, set.Service, func() {
		// Preserve API-server assigned fields (ClusterIP is immutable).
		set.Service.Spec.Type = desiredSvc.Spec.Type
		set.Service.Spec.Selector = desiredSvc.Spec.Selector
		set.Service.Spec.Ports = desiredSvc.Spec.Ports
		set.Service.Spec.LoadBalancerClass = desiredSvc.Spec.LoadBalancerClass
		set.Service.Labels = desiredSvc.Labels
		set.Service.Annotations = desiredSvc.Annotations
	}); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

// apply creates or updates obj, setting the Gateway as controller owner so
// Kubernetes garbage collects the resource set on Gateway deletion
// (gateway-namespace mode).
func (r *GatewayReconciler) apply(ctx context.Context, gw *gwapiv1.Gateway, obj client.Object, mutate func()) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		mutate()
		return controllerutil.SetControllerReference(gw, obj, r.Scheme())
	})
	return err
}

func (r *GatewayReconciler) observe(ctx context.Context, gw *gwapiv1.Gateway, set *provisioner.ResourceSet) (*corev1.Service, *appsv1.Deployment, error) {
	name := types.NamespacedName{Namespace: gw.Namespace, Name: set.Service.Name}

	var svc corev1.Service
	svcPtr := &svc
	if err := r.Get(ctx, name, &svc); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		svcPtr = nil
	}

	var dep appsv1.Deployment
	depPtr := &dep
	if err := r.Get(ctx, types.NamespacedName{Namespace: gw.Namespace, Name: set.Deployment.Name}, &dep); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		depPtr = nil
	}
	return svcPtr, depPtr, nil
}

func (r *GatewayReconciler) writeStatus(gw *gwapiv1.Gateway, svc *corev1.Service, dep *appsv1.Deployment) {
	res := status.Derive(gw, svc, dep)
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
