package controller

import (
	"context"
	"net"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/hybrid-gateway/internal/cilium"
	"github.com/aws/hybrid-gateway/internal/vxlan"
)

// VTEPConfigReconciler watches CiliumVTEPConfig and recreates it if deleted
// or corrects its values if they don't match the leader's current state.
type VTEPConfigReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	VxlanIface *vxlan.Interface
	NodeIP     net.IP
	VpcCIDRs   []string
	Logger     logr.Logger
}

// Reconcile calls UpsertCiliumVTEPConfig which handles both deletion and value correction.
func (r *VTEPConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if req.Name != cilium.CiliumVTEPConfigName {
		return ctrl.Result{}, nil
	}

	logger.Info("Reconciling CiliumVTEPConfig", "name", req.Name)
	if err := cilium.UpsertCiliumVTEPConfig(ctx, r.Client, r.VxlanIface, r.NodeIP, r.VpcCIDRs, r.Logger); err != nil {
		logger.Error(err, "Failed to upsert CiliumVTEPConfig")
		return ctrl.Result{}, err
	}
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

	return ctrl.NewControllerManagedBy(mgr).
		For(vtepConfig).
		WithOptions(controller.Options{
			NeedLeaderElection: ptr.To(true),
		}).
		Complete(r)
}
