package cilium

import (
	"context"
	"fmt"
	"net"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/hybrid-gateway/internal/vxlan"
)

var ciliumVTEPConfigGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumVTEPConfig",
}

const (
	CiliumVTEPConfigName = "hybrid-gateway"
	vtepEndpointName     = "vpc-gateway"
)

// UpsertCiliumVTEPConfig creates or updates the CiliumVTEPConfig CRD that tells
// Cilium to route traffic for vpcCIDRs via the leader gateway node.
// Each CIDR gets its own endpoint entry because the CRD schema validates that
// the cidr field contains a single CIDR value.
func UpsertCiliumVTEPConfig(
	ctx context.Context,
	k8sClient client.Client,
	vxlanIface *vxlan.Interface,
	nodeIP net.IP,
	vpcCIDRs []string,
	logger logr.Logger,
) error {
	macAddr := vxlanIface.GetMac()

	logger.Info("Upserting CiliumVTEPConfig",
		"name", CiliumVTEPConfigName,
		"tunnelEndpoint", nodeIP.String(),
		"mac", macAddr,
		"cidrs", vpcCIDRs,
	)

	endpoints := make([]interface{}, 0, len(vpcCIDRs))
	for i, cidr := range vpcCIDRs {
		name := vtepEndpointName
		if len(vpcCIDRs) > 1 {
			name = fmt.Sprintf("%s-%d", vtepEndpointName, i)
		}
		endpoints = append(endpoints, map[string]interface{}{
			"name":           name,
			"tunnelEndpoint": nodeIP.String(),
			"cidr":           cidr,
			"mac":            macAddr,
		})
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumVTEPConfig",
			"metadata": map[string]interface{}{
				"name": CiliumVTEPConfigName,
			},
			"spec": map[string]interface{}{
				"endpoints": endpoints,
			},
		},
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(ciliumVTEPConfigGVK)
	err := k8sClient.Get(ctx, client.ObjectKey{Name: CiliumVTEPConfigName}, existing)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("getting CiliumVTEPConfig %q: %w", CiliumVTEPConfigName, err)
		}

		desired.SetGroupVersionKind(ciliumVTEPConfigGVK)
		desired.SetCreationTimestamp(metav1.Time{})
		if createErr := k8sClient.Create(ctx, desired); createErr != nil {
			if k8serrors.IsAlreadyExists(createErr) {
				logger.Info("CiliumVTEPConfig already exists", "name", CiliumVTEPConfigName)
				return nil
			}
			return fmt.Errorf("creating CiliumVTEPConfig %q: %w", CiliumVTEPConfigName, createErr)
		}
		logger.Info("Created CiliumVTEPConfig", "name", CiliumVTEPConfigName)
		return nil
	}

	if !needsUpdate(existing, nodeIP.String(), macAddr, vpcCIDRs) {
		logger.Info("CiliumVTEPConfig already up to date", "name", CiliumVTEPConfigName)
		return nil
	}

	desired.SetGroupVersionKind(ciliumVTEPConfigGVK)
	desired.SetResourceVersion(existing.GetResourceVersion())
	desired.SetName(CiliumVTEPConfigName)

	if updateErr := k8sClient.Update(ctx, desired); updateErr != nil {
		return fmt.Errorf("updating CiliumVTEPConfig %q: %w", CiliumVTEPConfigName, updateErr)
	}

	logger.Info("Updated CiliumVTEPConfig", "name", CiliumVTEPConfigName)
	return nil
}

func needsUpdate(existing *unstructured.Unstructured, tunnelEndpoint, mac string, cidrs []string) bool {
	endpoints, found, _ := unstructured.NestedSlice(existing.Object, "spec", "endpoints")
	if !found || len(endpoints) != len(cidrs) {
		return true
	}

	for i, cidr := range cidrs {
		ep, ok := endpoints[i].(map[string]interface{})
		if !ok {
			return true
		}

		existingIP, _, _ := unstructured.NestedString(ep, "tunnelEndpoint")
		existingMAC, _, _ := unstructured.NestedString(ep, "mac")
		existingCIDR, _, _ := unstructured.NestedString(ep, "cidr")

		if existingIP != tunnelEndpoint || existingMAC != mac || existingCIDR != cidr {
			return true
		}
	}

	return false
}
