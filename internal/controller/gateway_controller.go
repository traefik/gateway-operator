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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
		Complete(r)
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/finalizers,verbs=update
// Read-only on GatewayClass: resolving the class a Gateway references and its
// parametersRef. Its status belongs to the data plane.
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.traefik.io,resources=traefikproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;bind

// Reconcile provisions the data plane for a Gateway. The provisioned data plane
// (the in-tree kubernetesGateway provider, scoped to this Gateway) is the single
// writer of Gateway, listener and route status; the operator only provisions
// infrastructure.
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

	set := provisioner.Render(&gw, proxy, gw.Namespace)
	if err := r.applySet(ctx, &gw, set); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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

func (r *GatewayReconciler) applySet(ctx context.Context, gw *gwapiv1.Gateway, set *provisioner.ResourceSet) error {
	// The data plane lives in the Gateway's own namespace, so every namespaced
	// resource carries an owner reference to the Gateway for garbage collection.
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
	if err := r.apply(ctx, gw, set.Service, true); err != nil {
		return fmt.Errorf("service: %w", err)
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
