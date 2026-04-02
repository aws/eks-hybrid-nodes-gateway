package cilium_test

import (
	"context"
	"net"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/aws/hybrid-gateway/internal/cilium"
	"github.com/aws/hybrid-gateway/internal/vxlan"
)

func stubVxlanIface() *vxlan.Interface {
	return vxlan.NewInterfaceWithMAC("aa:bb:cc:dd:ee:ff")
}

func newExistingVTEPConfig(tunnelEndpoint, cidr, mac string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumVTEPConfig",
			"metadata": map[string]interface{}{
				"name":            cilium.CiliumVTEPConfigName,
				"resourceVersion": "123",
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"name":           "vpc-gateway",
						"tunnelEndpoint": tunnelEndpoint,
						"cidr":           cidr,
						"mac":            mac,
					},
				},
			},
		},
	}
}

func TestUpsertCiliumVTEPConfig_CreatesWhenNotFound(t *testing.T) {
	ctx := context.Background()
	logger := testr.New(t)
	k8sClient := fake.NewClientBuilder().Build()

	err := cilium.UpsertCiliumVTEPConfig(ctx, k8sClient, stubVxlanIface(), net.ParseIP("10.0.1.5"), "10.0.0.0/16", logger)
	require.NoError(t, err)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumVTEPConfig"})
	err = k8sClient.Get(ctx, keyForVTEP(), obj)
	require.NoError(t, err)

	endpoints, found, _ := unstructured.NestedSlice(obj.Object, "spec", "endpoints")
	assert.True(t, found, "spec.endpoints should exist")
	assert.Len(t, endpoints, 1)

	ep := endpoints[0].(map[string]interface{})
	assert.Equal(t, "vpc-gateway", ep["name"])
	assert.Equal(t, "10.0.1.5", ep["tunnelEndpoint"])
	assert.Equal(t, "10.0.0.0/16", ep["cidr"])
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", ep["mac"])
}

func TestUpsertCiliumVTEPConfig_SkipsUpdateWhenUnchanged(t *testing.T) {
	ctx := context.Background()
	logger := testr.New(t)

	existing := newExistingVTEPConfig("10.0.1.5", "10.0.0.0/16", "aa:bb:cc:dd:ee:ff")
	k8sClient := fake.NewClientBuilder().WithObjects(existing).Build()

	updateCalled := false
	wrappedClient := interceptingClient(k8sClient, &updateCalled)

	err := cilium.UpsertCiliumVTEPConfig(ctx, wrappedClient, stubVxlanIface(), net.ParseIP("10.0.1.5"), "10.0.0.0/16", logger)
	require.NoError(t, err)
	assert.False(t, updateCalled, "should not call Update when values unchanged")
}

func TestUpsertCiliumVTEPConfig_UpdatesWhenChanged(t *testing.T) {
	tests := []struct {
		name         string
		existingIP   string
		existingCIDR string
		existingMAC  string
		newIP        string
		newCIDR      string
		newMAC       string
	}{
		{
			name:         "tunnelEndpoint changed",
			existingIP:   "10.0.1.5",
			existingCIDR: "10.0.0.0/16",
			existingMAC:  "aa:bb:cc:dd:ee:ff",
			newIP:        "10.0.2.10",
			newCIDR:      "10.0.0.0/16",
			newMAC:       "aa:bb:cc:dd:ee:ff",
		},
		{
			name:         "cidr changed",
			existingIP:   "10.0.1.5",
			existingCIDR: "10.0.0.0/16",
			existingMAC:  "aa:bb:cc:dd:ee:ff",
			newIP:        "10.0.1.5",
			newCIDR:      "10.20.0.0/16",
			newMAC:       "aa:bb:cc:dd:ee:ff",
		},
		{
			name:         "mac changed",
			existingIP:   "10.0.1.5",
			existingCIDR: "10.0.0.0/16",
			existingMAC:  "aa:bb:cc:dd:ee:ff",
			newIP:        "10.0.1.5",
			newCIDR:      "10.0.0.0/16",
			newMAC:       "11:22:33:44:55:66",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			logger := testr.New(t)

			existing := newExistingVTEPConfig(tt.existingIP, tt.existingCIDR, tt.existingMAC)
			k8sClient := fake.NewClientBuilder().WithObjects(existing).Build()

			iface := vxlan.NewInterfaceWithMAC(tt.newMAC)
			err := cilium.UpsertCiliumVTEPConfig(ctx, k8sClient, iface, net.ParseIP(tt.newIP), tt.newCIDR, logger)
			require.NoError(t, err)

			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumVTEPConfig"})
			err = k8sClient.Get(ctx, keyForVTEP(), obj)
			require.NoError(t, err)

			endpoints, _, _ := unstructured.NestedSlice(obj.Object, "spec", "endpoints")
			require.Len(t, endpoints, 1)

			ep := endpoints[0].(map[string]interface{})
			assert.Equal(t, tt.newIP, ep["tunnelEndpoint"])
			assert.Equal(t, tt.newCIDR, ep["cidr"])
			assert.Equal(t, tt.newMAC, ep["mac"])
		})
	}
}

func TestUpsertCiliumVTEPConfig_UpdatesWhenEndpointsEmpty(t *testing.T) {
	ctx := context.Background()
	logger := testr.New(t)

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumVTEPConfig",
			"metadata": map[string]interface{}{
				"name":            cilium.CiliumVTEPConfigName,
				"resourceVersion": "456",
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithObjects(existing).Build()

	err := cilium.UpsertCiliumVTEPConfig(ctx, k8sClient, stubVxlanIface(), net.ParseIP("10.0.1.5"), "10.0.0.0/16", logger)
	require.NoError(t, err)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumVTEPConfig"})
	err = k8sClient.Get(ctx, keyForVTEP(), obj)
	require.NoError(t, err)

	endpoints, _, _ := unstructured.NestedSlice(obj.Object, "spec", "endpoints")
	assert.Len(t, endpoints, 1)
}

func TestUpsertCiliumVTEPConfig_UpdatesWhenNoEndpointsField(t *testing.T) {
	ctx := context.Background()
	logger := testr.New(t)

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumVTEPConfig",
			"metadata": map[string]interface{}{
				"name":            cilium.CiliumVTEPConfigName,
				"resourceVersion": "789",
			},
			"spec": map[string]interface{}{},
		},
	}
	k8sClient := fake.NewClientBuilder().WithObjects(existing).Build()

	err := cilium.UpsertCiliumVTEPConfig(ctx, k8sClient, stubVxlanIface(), net.ParseIP("10.0.1.5"), "10.0.0.0/16", logger)
	require.NoError(t, err)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumVTEPConfig"})
	err = k8sClient.Get(ctx, keyForVTEP(), obj)
	require.NoError(t, err)

	endpoints, _, _ := unstructured.NestedSlice(obj.Object, "spec", "endpoints")
	assert.Len(t, endpoints, 1)
}

func keyForVTEP() client.ObjectKey {
	return client.ObjectKey{Name: cilium.CiliumVTEPConfigName}
}

// interceptingClient wraps a client to track whether Update was called.
func interceptingClient(inner client.WithWatch, updateCalled *bool) client.Client {
	return interceptor.NewClient(inner, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			*updateCalled = true
			return c.Update(ctx, obj, opts...)
		},
	})
}
