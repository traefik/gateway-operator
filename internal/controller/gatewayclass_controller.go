// Package controller implements the operator reconcilers.
package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/operator/internal/provisioner"
)

// GatewayClassReconciler accepts the GatewayClasses claimed by the operator.
type GatewayClassReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses/status,verbs=update;patch

// Reconcile sets the Accepted condition on GatewayClasses owned by the operator.
func (r *GatewayClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gc gwapiv1.GatewayClass
	if err := r.Get(ctx, req.NamespacedName, &gc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if gc.Spec.ControllerName != provisioner.ControllerName {
		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&gc.Status.Conditions, metav1.Condition{
		Type:               string(gwapiv1.GatewayClassConditionStatusAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gwapiv1.GatewayClassReasonAccepted),
		Message:            "Handled by traefik-operator",
		ObservedGeneration: gc.Generation,
	})
	gc.Status.SupportedFeatures = supportedFeatures()

	if err := r.Status().Update(ctx, &gc); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler.
func (r *GatewayClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.GatewayClass{}).
		Complete(r)
}
