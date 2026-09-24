package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/gateway-operator/api/v1alpha1"
	"github.com/traefik/gateway-operator/internal/provisioner"
)

// dataPlaneFinalizer guards the data plane's cluster-scoped ClusterRoleBinding,
// which cannot be garbage collected through the namespaced Gateway owner
// reference and so is deleted explicitly on Gateway removal.
const dataPlaneFinalizer = "gateway.traefik.io/data-plane"

// GatewayReconciler provisions a data plane per Gateway and owns its status.
type GatewayReconciler struct {
	client.Client
}

// SetupWithManager registers the reconciler and its owned resources.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.Gateway{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		// A ListenerSet adds its listener ports to the data plane of its Gateway.
		Watches(&gwapiv1.ListenerSet{}, handler.EnqueueRequestsFromMapFunc(listenerSetGateway)).
		Complete(r)
}

// listenerSetGateway enqueues the Gateway a ListenerSet references.
func listenerSetGateway(_ context.Context, obj client.Object) []ctrl.Request {
	ls, ok := obj.(*gwapiv1.ListenerSet)
	if !ok || !isGatewayRef(ls.Spec.ParentRef) {
		return nil
	}
	return []ctrl.Request{{NamespacedName: listenerSetParent(ls)}}
}

// listenerSetParent returns the Gateway a ListenerSet references, which
// defaults to the ListenerSet namespace.
func listenerSetParent(ls *gwapiv1.ListenerSet) types.NamespacedName {
	return types.NamespacedName{
		Namespace: string(ptr.Deref(ls.Spec.ParentRef.Namespace, gwapiv1.Namespace(ls.Namespace))),
		Name:      string(ls.Spec.ParentRef.Name),
	}
}

func isGatewayRef(ref gwapiv1.ParentGatewayReference) bool {
	return ptr.Deref(ref.Group, gwapiv1.GroupName) == gwapiv1.GroupName &&
		ptr.Deref(ref.Kind, "Gateway") == "Gateway"
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/finalizers,verbs=update
// Read-only on GatewayClass: resolving the class a Gateway references and its
// parametersRef. Its status belongs to the data plane.
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.traefik.io,resources=traefikproxies,verbs=get;list;watch
// Read-only on ListenerSets, whose listener ports the data plane exposes, and on
// Namespaces, to evaluate a Gateway allowedListeners namespace selector.
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=listenersets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;bind

// Reconcile provisions the data plane for a Gateway. The provisioned data plane
// (the in-tree kubernetesGateway provider, scoped to this Gateway) writes the
// Gateway, listener and route status, including addresses. The operator only
// writes the Gateway status when a static address is rejected for an
// unsupported type: in that case no data plane is provisioned and the operator
// is the sole status writer.
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
		// finalizer and orphaned resources. Clean up.
		if controllerutil.ContainsFinalizer(&gw, dataPlaneFinalizer) {
			return ctrl.Result{}, r.finalize(ctx, &gw)
		}
		return ctrl.Result{}, nil
	}

	proxy, err := r.resolveProxy(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !gw.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&gw, dataPlaneFinalizer) {
			return ctrl.Result{}, r.finalize(ctx, &gw)
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&gw, dataPlaneFinalizer) {
		if err := r.Update(ctx, &gw); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, r.provisionGateway(ctx, &gw, proxy)
}

// provisionGateway renders and applies the data plane for a managed, live
// Gateway, and reconciles the static-address portion of its status.
func (r *GatewayReconciler) provisionGateway(ctx context.Context, gw *gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy) error {
	// A Gateway that requests an unsupported static address type is rejected up
	// front: the configuration can never be satisfied, so no data plane may run
	// with it. Any previously provisioned data plane is torn down (the finalizer
	// is kept for the cluster-scoped binding) and the operator publishes the
	// Gateway status itself, since no data plane is left to write it. This is
	// decided from the spec alone, so it needs no Service.
	if conds := provisioner.StaticAddressRejection(gw); conds != nil {
		if err := r.cleanup(ctx, gw, gw.Namespace); err != nil {
			return err
		}
		// Nil addresses clear anything a torn-down data plane left behind.
		return r.writeGatewayStatus(ctx, gw, nil, conds)
	}

	listenerSets, err := r.attachedListenerSets(ctx, gw)
	if err != nil {
		return err
	}

	set := provisioner.Render(gw, listenerSets, proxy, gw.Namespace)

	// The Service is provisioned first: it is what an external load balancer
	// assigns an address to, and observing which requested static addresses it
	// realized decides whether the data plane may run.
	if err := r.applyInfra(ctx, gw, set); err != nil {
		return err
	}

	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: set.Service.Namespace, Name: set.Service.Name}, &svc); err != nil {
		return client.IgnoreNotFound(err)
	}

	addrs, conds, provision := provisioner.StaticAddressStatus(gw, provisioner.RealizedAddresses(&svc))
	if !provision {
		// A requested static address is not usable: no data plane runs, and the
		// operator publishes the address status itself.
		if err := r.deleteDataPlane(ctx, set); err != nil {
			return err
		}
		return r.writeGatewayStatus(ctx, gw, addrs, conds)
	}

	// Addresses are usable, or none were requested: run the data plane and let
	// it own the Gateway status. Its full status update overwrites any error
	// status the operator published earlier, so there is nothing to release: the
	// operator simply stops writing the status. Until the data plane comes up,
	// the operator's last conditions stay in place at the current generation.
	return r.applyDataPlane(ctx, gw, set)
}

// writeGatewayStatus server-side applies the Gateway status the operator owns
// while it handles a static-address error, as no data plane runs to write it:
// the addresses to publish and the Accepted/Programmed conditions. status.
// addresses is always asserted (an explicit empty list clears anything a
// torn-down data plane left; the typed field is omitempty and a nil would leave
// the stale value).
func (r *GatewayReconciler) writeGatewayStatus(ctx context.Context, gw *gwapiv1.Gateway, addrs []gwapiv1.GatewayStatusAddress, conds []metav1.Condition) error {
	addrItems := make([]any, 0, len(addrs))
	for i := range addrs {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&addrs[i])
		if err != nil {
			return err
		}
		addrItems = append(addrItems, m)
	}

	// Preserve the transition time when a condition's status is unchanged to
	// avoid rewriting the Gateway on every reconcile.
	condItems := make([]any, 0, len(conds))
	for i := range conds {
		if existing := apimeta.FindStatusCondition(gw.Status.Conditions, conds[i].Type); existing != nil && existing.Status == conds[i].Status {
			conds[i].LastTransitionTime = existing.LastTransitionTime
		} else {
			conds[i].LastTransitionTime = metav1.Now()
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&conds[i])
		if err != nil {
			return err
		}
		condItems = append(condItems, m)
	}

	return r.applyGatewayStatus(ctx, gw, map[string]any{
		"addresses":  addrItems,
		"conditions": condItems,
	})
}

// applyGatewayStatus server-side applies the given Gateway status fields under
// the operator's field manager. Fields absent from status are released by the
// operator, so the data plane can own everything the operator does not set.
func (r *GatewayReconciler) applyGatewayStatus(ctx context.Context, gw *gwapiv1.Gateway, status map[string]any) error {
	gvk, err := apiutil.GVKForObject(&gwapiv1.Gateway{}, r.Scheme())
	if err != nil {
		return err
	}
	apply := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      gw.Name,
			"namespace": gw.Namespace,
		},
		"status": status,
	}}
	return r.Status().Patch(ctx, apply, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership)
}

// finalize deletes the data plane's cluster-scoped ClusterRoleBinding (the only
// resource not garbage collected through the Gateway owner reference) and
// removes the finalizer.
func (r *GatewayReconciler) finalize(ctx context.Context, gw *gwapiv1.Gateway) error {
	if err := r.cleanup(ctx, gw, gw.Namespace); err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(gw, dataPlaneFinalizer)
	return r.Update(ctx, gw)
}

// cleanup deletes the data plane resources provisioned for a Gateway in the
// given namespace. The resource set is a single named object of each kind.
func (r *GatewayReconciler) cleanup(ctx context.Context, gw *gwapiv1.Gateway, namespace string) error {
	return r.deleteSet(ctx, namespace, provisioner.ResourceName(gw))
}

// deleteSet deletes the named data plane resource set in a namespace.
func (r *GatewayReconciler) deleteSet(ctx context.Context, namespace, name string) error {
	om := metav1.ObjectMeta{Name: name, Namespace: namespace}
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: om},
		&corev1.Service{ObjectMeta: om},
		&corev1.ServiceAccount{ObjectMeta: om},
		&policyv1.PodDisruptionBudget{ObjectMeta: om},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om},
		// Cluster-scoped, named the same, no namespace.
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}},
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

// attachedListenerSets returns the ListenerSets referencing the Gateway and
// allowed by its allowedListeners, which defaults to none.
func (r *GatewayReconciler) attachedListenerSets(ctx context.Context, gw *gwapiv1.Gateway) ([]gwapiv1.ListenerSet, error) {
	if gw.Spec.AllowedListeners == nil || gw.Spec.AllowedListeners.Namespaces == nil {
		return nil, nil
	}
	allowed := gw.Spec.AllowedListeners.Namespaces
	from := ptr.Deref(allowed.From, gwapiv1.NamespacesFromNone)
	if from == gwapiv1.NamespacesFromNone {
		return nil, nil
	}

	var selector labels.Selector
	if from == gwapiv1.NamespacesFromSelector {
		var err error
		if selector, err = metav1.LabelSelectorAsSelector(allowed.Selector); err != nil {
			return nil, fmt.Errorf("allowedListeners namespace selector: %w", err)
		}
	}

	var list gwapiv1.ListenerSetList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}

	var attached []gwapiv1.ListenerSet
	for _, ls := range list.Items {
		if !isGatewayRef(ls.Spec.ParentRef) || listenerSetParent(&ls) != (types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}) {
			continue
		}

		ok, err := r.namespaceAllowed(ctx, from, selector, gw.Namespace, ls.Namespace)
		if err != nil {
			return nil, err
		}
		if ok {
			attached = append(attached, ls)
		}
	}
	return attached, nil
}

// namespaceAllowed reports whether a Gateway in gwNamespace allows the
// ListenerSets of namespace, as configured by its allowedListeners.
func (r *GatewayReconciler) namespaceAllowed(ctx context.Context, from gwapiv1.FromNamespaces, selector labels.Selector, gwNamespace, namespace string) (bool, error) {
	switch from {
	case gwapiv1.NamespacesFromAll:
		return true, nil
	case gwapiv1.NamespacesFromSame:
		return namespace == gwNamespace, nil
	case gwapiv1.NamespacesFromSelector:
		var ns corev1.Namespace
		if err := r.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return selector.Matches(labels.Set(ns.Labels)), nil
	default:
		return false, nil
	}
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

// fieldManager identifies the operator as the server-side apply owner of the
// fields it manages.
const fieldManager = "traefik-gateway-operator"

// applyInfra provisions the Gateway's Service. It is applied before the data
// plane so an external load balancer can assign it an address, which decides
// whether the data plane may run (see provisioner.StaticAddressStatus). The
// Service carries an owner reference to the Gateway for garbage collection.
func (r *GatewayReconciler) applyInfra(ctx context.Context, gw *gwapiv1.Gateway, set *provisioner.ResourceSet) error {
	if err := r.apply(ctx, gw, set.Service, true); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

// applyDataPlane provisions the data plane that runs Traefik: its
// ServiceAccount and cluster-scoped binding, the Deployment, and the optional
// PodDisruptionBudget and HorizontalPodAutoscaler. Every namespaced resource
// carries an owner reference to the Gateway for garbage collection.
func (r *GatewayReconciler) applyDataPlane(ctx context.Context, gw *gwapiv1.Gateway, set *provisioner.ResourceSet) error {
	if err := r.apply(ctx, gw, set.ServiceAccount, true); err != nil {
		return fmt.Errorf("serviceaccount: %w", err)
	}
	// The ClusterRoleBinding is cluster-scoped and cannot be owned by the
	// namespaced Gateway; it is reclaimed by the finalizer instead.
	if err := r.apply(ctx, gw, set.ClusterRoleBinding, false); err != nil {
		return fmt.Errorf("clusterrolebinding: %w", err)
	}
	if err := r.apply(ctx, gw, set.Deployment, true); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}

	om := metav1.ObjectMeta{Name: set.Service.Name, Namespace: set.Service.Namespace}

	if set.PodDisruptionBudget != nil {
		if err := r.apply(ctx, gw, set.PodDisruptionBudget, true); err != nil {
			return fmt.Errorf("poddisruptionbudget: %w", err)
		}
	} else if err := r.deleteIfExists(ctx, &policyv1.PodDisruptionBudget{ObjectMeta: om}); err != nil {
		return fmt.Errorf("poddisruptionbudget: %w", err)
	}

	if set.HPA != nil {
		if err := r.apply(ctx, gw, set.HPA, true); err != nil {
			return fmt.Errorf("horizontalpodautoscaler: %w", err)
		}
	} else if err := r.deleteIfExists(ctx, &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om}); err != nil {
		return fmt.Errorf("horizontalpodautoscaler: %w", err)
	}

	return nil
}

// deleteDataPlane removes the data plane that runs Traefik while keeping the
// Service, so a Gateway whose static addresses cannot be honored runs no
// Traefik yet still exposes the Service an address is assigned to.
func (r *GatewayReconciler) deleteDataPlane(ctx context.Context, set *provisioner.ResourceSet) error {
	name := set.Service.Name
	om := metav1.ObjectMeta{Name: name, Namespace: set.Service.Namespace}
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: om},
		&policyv1.PodDisruptionBudget{ObjectMeta: om},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om},
		&corev1.ServiceAccount{ObjectMeta: om},
		// Cluster-scoped, named the same, no namespace.
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}},
	} {
		if err := r.deleteIfExists(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

// deleteIfExists deletes obj, ignoring a not-found result.
func (r *GatewayReconciler) deleteIfExists(ctx context.Context, obj client.Object) error {
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// apply server-side applies the desired object. Server-side apply only owns the
// fields the operator sets, leaving server-defaulted fields untouched, which
// avoids the update churn a full-spec overwrite would cause. When owner is true
// the Gateway is set as controller owner for garbage collection.
func (r *GatewayReconciler) apply(ctx context.Context, gw *gwapiv1.Gateway, obj client.Object, owner bool) error {
	if owner {
		if err := controllerutil.SetControllerReference(gw, obj, r.Scheme()); err != nil {
			return err
		}
	}

	gvk, err := apiutil.GVKForObject(obj, r.Scheme())
	if err != nil {
		return err
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)

	return r.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership)
}
