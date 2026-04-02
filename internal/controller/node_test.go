package controller_test

import (
	"testing"

	"github.com/cilium/cilium/pkg/ipam/types"
	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/node/addressing"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/hybrid-gateway/internal/controller"
)

func TestIsHybridNode(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected bool
	}{
		{
			name:     "hybrid node",
			labels:   map[string]string{controller.HybridNodeLabel: controller.HybridNodeValue},
			expected: true,
		},
		{
			name:     "auto node is not hybrid",
			labels:   map[string]string{controller.HybridNodeLabel: "auto"},
			expected: false,
		},
		{
			name:     "no labels",
			labels:   nil,
			expected: false,
		},
		{
			name:     "unrelated labels",
			labels:   map[string]string{"kubernetes.io/os": "linux"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ciliumv2.CiliumNode{
				ObjectMeta: metav1.ObjectMeta{
					Labels: tt.labels,
				},
			}
			assert.Equal(t, tt.expected, controller.IsHybridNode(node))
		})
	}
}

func TestGetNodeInternalIP(t *testing.T) {
	tests := []struct {
		name      string
		addresses []ciliumv2.NodeAddress
		expected  string
	}{
		{
			name: "picks InternalIP from mixed addresses",
			addresses: []ciliumv2.NodeAddress{
				{Type: addressing.NodeCiliumInternalIP, IP: "10.86.232.55"},
				{Type: addressing.NodeInternalIP, IP: "172.31.41.35"},
			},
			expected: "172.31.41.35",
		},
		{
			name: "returns first InternalIP when multiple exist",
			addresses: []ciliumv2.NodeAddress{
				{Type: addressing.NodeInternalIP, IP: "172.31.41.35"},
				{Type: addressing.NodeInternalIP, IP: "172.31.41.36"},
			},
			expected: "172.31.41.35",
		},
		{
			name: "ignores CiliumInternalIP",
			addresses: []ciliumv2.NodeAddress{
				{Type: addressing.NodeCiliumInternalIP, IP: "10.86.232.55"},
			},
			expected: "",
		},
		{
			name:      "no addresses returns empty",
			addresses: nil,
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ciliumv2.CiliumNode{
				Spec: ciliumv2.NodeSpec{
					Addresses: tt.addresses,
				},
			}
			assert.Equal(t, tt.expected, controller.GetNodeInternalIP(node))
		})
	}
}

func TestGetPodCIDR(t *testing.T) {
	tests := []struct {
		name        string
		podCIDRs    []string
		expected    string
		expectedErr bool
	}{
		{
			name:     "returns first pod CIDR",
			podCIDRs: []string{"10.86.232.0/25"},
			expected: "10.86.232.0/25",
		},
		{
			name:     "multiple CIDRs returns first",
			podCIDRs: []string{"10.86.232.0/25", "10.86.233.0/25"},
			expected: "10.86.232.0/25",
		},
		{
			name:        "empty CIDRs returns error",
			podCIDRs:    nil,
			expected:    "",
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ciliumv2.CiliumNode{
				Spec: ciliumv2.NodeSpec{
					IPAM: types.IPAMSpec{
						PodCIDRs: tt.podCIDRs,
					},
				},
			}
			result, err := controller.GetPodCIDR(node)
			if tt.expectedErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
