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
// Cilium to route traffic for vpcCIDR via the leader gateway node.
func UpsertCiliumVTEPConfig(
	ctx context.Context,
	k8sClient client.Client,
	vxlanIface *vxlan.Interface,
	nodeIP net.IP,
	vpcCIDR string,
	logger logr.Logger,
) error {
	macAddr := vxlanIface.GetMac()

	logger.Info("Upserting CiliumVTEPConfig",
		"name", CiliumVTEPConfigName,
		"tunnelEndpoint", nodeIP.String(),
		"mac", macAddr,
		"cidr", vpcCIDR,
	)

	endpoint := map[string]interface{}{
		"name":           vtepEndpointName,
		"tunnelEndpoint": nodeIP.String(),
		"cidr":           vpcCIDR,
		"mac":            macAddr,
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumVTEPConfig",
			"metadata": map[string]interface{}{
				"name": CiliumVTEPConfigName,
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{endpoint},
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
			return fmt.Errorf("creating CiliumVTEPConfig %q: %w", CiliumVTEPConfigName, createErr)
		}
		logger.Info("Created CiliumVTEPConfig", "name", CiliumVTEPConfigName)
		return nil
	}

	if !needsUpdate(existing, nodeIP.String(), macAddr, vpcCIDR) {
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

func needsUpdate(existing *unstructured.Unstructured, tunnelEndpoint, mac, cidr string) bool {
	endpoints, found, _ := unstructured.NestedSlice(existing.Object, "spec", "endpoints")
	if !found || len(endpoints) == 0 {
		return true
	}

	ep, ok := endpoints[0].(map[string]interface{})
	if !ok {
		return true
	}

	existingIP, _, _ := unstructured.NestedString(ep, "tunnelEndpoint")
	existingMAC, _, _ := unstructured.NestedString(ep, "mac")
	existingCIDR, _, _ := unstructured.NestedString(ep, "cidr")

	return existingIP != tunnelEndpoint || existingMAC != mac || existingCIDR != cidr
}
