package controller_test

import (
	"context"
	"net"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aws/hybrid-gateway/internal/cilium"
	"github.com/aws/hybrid-gateway/internal/controller"
	"github.com/aws/hybrid-gateway/internal/vxlan"
)

const (
	testNodeIP  = "10.0.2.99"
	testMAC     = "02:00:0a:00:02:63"
	testVPCCIDR = "10.0.0.0/16"
)

var vtepGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumVTEPConfig",
}

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(vtepGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "cilium.io",
		Version: "v2",
		Kind:    "CiliumVTEPConfigList",
	}, &unstructured.UnstructuredList{})
	return scheme
}

func TestVTEPConfigReconciler_IgnoresOtherNames(t *testing.T) {
	scheme := newTestScheme()
	r := &controller.VTEPConfigReconciler{
		Client:     fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:     scheme,
		VxlanIface: vxlan.NewInterfaceWithMAC(testMAC),
		NodeIP:     net.ParseIP(testNodeIP),
		VpcCIDRs:   []string{testVPCCIDR},
		Logger:     logr.Discard(),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "some-other-config"},
	})

	assert.NoError(t, err)
}

func TestVTEPConfigReconciler_RecreatesWhenDeleted(t *testing.T) {
	scheme := newTestScheme()
	r := &controller.VTEPConfigReconciler{
		Client:     fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:     scheme,
		VxlanIface: vxlan.NewInterfaceWithMAC(testMAC),
		NodeIP:     net.ParseIP(testNodeIP),
		VpcCIDRs:   []string{testVPCCIDR},
		Logger:     logr.Discard(),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cilium.CiliumVTEPConfigName},
	})

	assert.NoError(t, err)

	// Verify Reconcile wired through to Upsert and the CR exists
	created := &unstructured.Unstructured{}
	created.SetGroupVersionKind(vtepGVK)
	err = r.Get(context.Background(), types.NamespacedName{Name: cilium.CiliumVTEPConfigName}, created)
	require.NoError(t, err)

	endpoints, found, err := unstructured.NestedSlice(created.Object, "spec", "endpoints")
	require.NoError(t, err)
	require.True(t, found, "spec.endpoints not found")
	require.Len(t, endpoints, 1)

	ep := endpoints[0].(map[string]interface{})
	assert.Equal(t, testNodeIP, ep["tunnelEndpoint"])
	assert.Equal(t, testMAC, ep["mac"])
	assert.Equal(t, testVPCCIDR, ep["cidr"])
}
