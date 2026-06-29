package controller

import (
	"context"
	"sort"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
	"github.com/traefik/traefik-operator/internal/provisioner"
)

// reconcileMerged provisions and maintains the single data plane shared by all
// Gateways of a merged GatewayClass, and writes the status of the reconciled
// Gateway.
func (r *GatewayReconciler) reconcileMerged(ctx context.Context, gw *gwapiv1.Gateway, proxy *v1alpha1.TraefikProxy, target provisioner.Target) (ctrl.Result, error) {
	// Live members of the merged group: Gateways of the class that are not
	// being deleted. A Gateway being deleted carries a DeletionTimestamp and is
	// excluded, so the shared data plane shrinks as members leave.
	members, err := r.mergedMembers(ctx, target.MergeKey)
	if err != nil {
		return ctrl.Result{}, err
	}

	mergedName := provisioner.MergedResourceName(target.MergeKey)

	// Deletion of this Gateway: shrink or tear down the shared data plane, then
	// release the finalizer.
	if !gw.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(gw, dataPlaneFinalizer) {
			if len(members) == 0 {
				if err := r.deleteSet(ctx, target.Namespace, mergedName); err != nil {
					return ctrl.Result{}, err
				}
			} else {
				set := provisioner.RenderMerged(target.MergeKey, members, proxy, target.Namespace)
				if err := r.applySet(ctx, gw, set, target); err != nil {
					return ctrl.Result{}, err
				}
			}
			controllerutil.RemoveFinalizer(gw, dataPlaneFinalizer)
			delete(gw.Annotations, provisionedNamespaceAnnotation)
			if err := r.Update(ctx, gw); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if changed := r.reconcileFinalizer(gw, target); changed {
		if err := r.Update(ctx, gw); err != nil {
			return ctrl.Result{}, err
		}
	}

	set := provisioner.RenderMerged(target.MergeKey, members, proxy, target.Namespace)
	if err := r.applySet(ctx, gw, set, target); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// mergedMembers returns the live Gateways of a merged GatewayClass, ordered
// deterministically.
func (r *GatewayReconciler) mergedMembers(ctx context.Context, class string) ([]*gwapiv1.Gateway, error) {
	var list gwapiv1.GatewayList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}

	var members []*gwapiv1.Gateway
	for i := range list.Items {
		g := &list.Items[i]
		if string(g.Spec.GatewayClassName) != class {
			continue
		}
		if !g.DeletionTimestamp.IsZero() {
			continue
		}
		members = append(members, g)
	}

	sort.Slice(members, func(i, j int) bool {
		if members[i].Namespace != members[j].Namespace {
			return members[i].Namespace < members[j].Namespace
		}
		return members[i].Name < members[j].Name
	})
	return members, nil
}
