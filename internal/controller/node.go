package controller

import (
	"context"
	"fmt"

	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/node/addressing"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/hybrid-gateway/internal/vxlan"
)

const (
	HybridNodeLabel = "eks.amazonaws.com/compute-type"
	HybridNodeValue = "hybrid"
)

// NodeReconciler reconciles CiliumNode objects for hybrid nodes.
// Non-hybrid CiliumNodes are skipped via label check.
type NodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	VTEP   *vxlan.VTEP
}

// Reconcile handles CiliumNode events
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ciliumNode ciliumv2.CiliumNode
	if err := r.Get(ctx, req.NamespacedName, &ciliumNode); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("Processing CiliumNode", "node", ciliumNode.Name)

	if !IsHybridNode(&ciliumNode) {
		logger.V(1).Info("Skipping non-hybrid CiliumNode", "node", ciliumNode.Name)
		return ctrl.Result{}, nil
	}

	nodeIP := GetNodeInternalIP(&ciliumNode)
	if nodeIP == "" {
		logger.Error(nil, "CiliumNode has no internal IP", "node", ciliumNode.Name)
		return ctrl.Result{}, fmt.Errorf("CiliumNode %s has no internal IP", ciliumNode.Name)
	}

	podCIDR, err := GetPodCIDR(&ciliumNode)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !ciliumNode.DeletionTimestamp.IsZero() {
		logger.Info("Removing hybrid node from gateway", "node", ciliumNode.Name, "ip", nodeIP)
		if err := r.VTEP.RemoveRemoteNode(podCIDR, nodeIP); err != nil {
			logger.Error(err, "Failed to remove hybrid node", "node", ciliumNode.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	logger.Info("Adding hybrid node to gateway", "node", ciliumNode.Name, "ip", nodeIP, "podCIDR", podCIDR)
	if err := r.VTEP.AddRemoteNode(podCIDR, nodeIP); err != nil {
		logger.Error(err, "Failed to add hybrid node", "node", ciliumNode.Name)
		return ctrl.Result{}, err
	}

	logger.Info("Successfully configured gateway for hybrid node", "node", ciliumNode.Name)
	return ctrl.Result{}, nil
}

// IsHybridNode returns true if the CiliumNode belongs to a hybrid node.
func IsHybridNode(node *ciliumv2.CiliumNode) bool {
	return node.Labels[HybridNodeLabel] == HybridNodeValue
}

// GetNodeInternalIP returns the internal IP address from a CiliumNode.
func GetNodeInternalIP(node *ciliumv2.CiliumNode) string {
	for _, addr := range node.Spec.Addresses {
		if addr.Type == addressing.NodeInternalIP {
			return addr.IP
		}
	}
	return ""
}

// GetPodCIDR returns the first pod CIDR allocated by Cilium IPAM.
func GetPodCIDR(node *ciliumv2.CiliumNode) (string, error) {
	if len(node.Spec.IPAM.PodCIDRs) > 0 {
		return node.Spec.IPAM.PodCIDRs[0], nil
	}
	return "", fmt.Errorf("CiliumNode %s has no pod CIDRs allocated", node.Name)
}

// SetupWithManager sets up the controller with the Manager.
// NeedLeaderElection is set to false so the reconciler runs on every gateway
// node, not just the leader, ensuring FDB entries and pod-CIDR routes are
// always programmed regardless of leader status.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	leaderElectionNotRequired := false
	return ctrl.NewControllerManagedBy(mgr).
		For(&ciliumv2.CiliumNode{}).
		WithOptions(controller.Options{
			NeedLeaderElection: &leaderElectionNotRequired,
		}).
		Complete(r)
}
