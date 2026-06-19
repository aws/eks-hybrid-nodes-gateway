package controller

import (
	"context"
	"net"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/hybrid-gateway/internal/cilium"
	"github.com/aws/hybrid-gateway/internal/vxlan"
)

// VTEPConfigReconciler watches CiliumVTEPConfig and recreates it if deleted.
type VTEPConfigReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	VxlanIface *vxlan.Interface
	NodeIP     net.IP
	VpcCIDRs   []string
	Logger     logr.Logger
}

// Reconcile recreates the CiliumVTEPConfig if it was deleted.
func (r *VTEPConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if req.Name != cilium.CiliumVTEPConfigName {
		return ctrl.Result{}, nil
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cilium.io",
		Version: "v2",
		Kind:    "CiliumVTEPConfig",
	})

	err := r.Get(ctx, req.NamespacedName, obj)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	if err != nil {
		// NotFound — recreate it
		logger.Info("CiliumVTEPConfig deleted, recreating", "name", req.Name)
		if upsertErr := cilium.UpsertCiliumVTEPConfig(
			ctx, r.Client, r.VxlanIface, r.NodeIP, r.VpcCIDRs, r.Logger,
		); upsertErr != nil {
			logger.Error(upsertErr, "Failed to recreate CiliumVTEPConfig")
			return ctrl.Result{}, upsertErr
		}
		return ctrl.Result{}, nil
	}

	// Exists — nothing to do
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller. Leader-only.
func (r *VTEPConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	vtepConfig := &unstructured.Unstructured{}
	vtepConfig.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cilium.io",
		Version: "v2",
		Kind:    "CiliumVTEPConfig",
	})

	leaderElectionRequired := true
	return ctrl.NewControllerManagedBy(mgr).
		For(vtepConfig).
		WithOptions(controller.Options{
			NeedLeaderElection: &leaderElectionRequired,
		}).
		Complete(r)
}
