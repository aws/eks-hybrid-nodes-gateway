package vxlan

import (
	"fmt"
	"net"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"

	gwmetrics "github.com/aws/hybrid-gateway/internal/metrics"
)

const (
	NUD_PERMANENT     = 0x80
	NTF_SELF          = 0x02
	RT_SCOPE_UNIVERSE = 0
	FLAG_ONLINK       = 0x04
	AF_BRIDGE         = 7
	AF_INET           = 2
)

type VTEP struct {
	iface  *Interface
	logger logr.Logger
	nodes  map[string]struct{}
}

func NewVTEP(iface *Interface) *VTEP {
	return &VTEP{iface: iface, logger: iface.logger, nodes: make(map[string]struct{})}
}

// AddRemoteNode adds a remote hybrid node as a VXLAN tunnel endpoint.
func (v *VTEP) AddRemoteNode(podCIDR, nodeIP string) error {
	v.logger.Info("Adding remote VTEP", "podCIDR", podCIDR, "nodeIP", nodeIP)

	link := v.iface.GetLink()
	if link == nil {
		return fmt.Errorf("VXLAN interface not initialized")
	}

	remoteIP := net.ParseIP(nodeIP)
	if remoteIP == nil {
		return fmt.Errorf("invalid remote IP: %s", nodeIP)
	}

	_, podNet, err := net.ParseCIDR(podCIDR)
	if err != nil {
		return fmt.Errorf("invalid pod CIDR: %v", err)
	}

	linkIdx := link.Attrs().Index

	// Route: pod CIDR via remote node IP through VXLAN (onlink avoids ARP for gateway)
	route := &netlink.Route{
		LinkIndex: linkIdx,
		Dst:       podNet,
		Scope:     netlink.Scope(RT_SCOPE_UNIVERSE),
		Gw:        remoteIP,
		Flags:     int(FLAG_ONLINK),
	}
	if err := netlink.RouteReplace(route); err != nil {
		gwmetrics.VTEPAddErrorsTotal.Inc()
		return fmt.Errorf("failed to add/update route for %s: %w", podCIDR, err)
	}

	// Static ARP: prevents kernel ARP lookups that would learn wrong MAC from Cilium
	uniqueMAC := generateMACFromIP(remoteIP)
	if err := netlink.NeighSet(&netlink.Neigh{
		LinkIndex:    linkIdx,
		Family:       AF_INET,
		State:        NUD_PERMANENT,
		IP:           remoteIP,
		HardwareAddr: uniqueMAC,
	}); err != nil {
		gwmetrics.VTEPAddErrorsTotal.Inc()
		return fmt.Errorf("failed to add static ARP entry for %s: %w", nodeIP, err)
	}

	// FDB: map the node's unique MAC to its VTEP IP so the VXLAN module sends
	// only to the correct node instead of broadcasting to all remote endpoints.
	if err := netlink.NeighSet(&netlink.Neigh{
		LinkIndex:    linkIdx,
		Family:       AF_BRIDGE,
		State:        NUD_PERMANENT,
		Flags:        NTF_SELF,
		IP:           remoteIP,
		HardwareAddr: uniqueMAC,
	}); err != nil {
		gwmetrics.VTEPAddErrorsTotal.Inc()
		return fmt.Errorf("failed to add/update FDB entry for %s: %w", nodeIP, err)
	}

	v.logger.Info("Remote VTEP added", "podCIDR", podCIDR, "nodeIP", nodeIP, "mac", uniqueMAC)

	// Metrics: increment VTEP add counter
	gwmetrics.VTEPAddTotal.Inc()
	v.nodes[nodeIP] = struct{}{}
	gwmetrics.HybridNodesConfigured.Set(float64(len(v.nodes)))

	return nil
}

// RemoveRemoteNode removes a remote hybrid node's route, ARP, and FDB entries.
func (v *VTEP) RemoveRemoteNode(podCIDR, nodeIP string) error {
	v.logger.Info("Removing remote VTEP", "podCIDR", podCIDR, "nodeIP", nodeIP)

	link := v.iface.GetLink()
	if link == nil {
		return fmt.Errorf("VXLAN interface not initialized")
	}

	remoteIP := net.ParseIP(nodeIP)
	if remoteIP == nil {
		return fmt.Errorf("invalid remote IP: %s", nodeIP)
	}

	_, podNet, err := net.ParseCIDR(podCIDR)
	if err != nil {
		return fmt.Errorf("invalid pod CIDR: %v", err)
	}

	linkIdx := link.Attrs().Index

	if err := netlink.RouteDel(&netlink.Route{LinkIndex: linkIdx, Dst: podNet}); err != nil {
		gwmetrics.VTEPRemoveErrorsTotal.Inc()
		v.logger.Error(err, "Warning: failed to remove route", "podCIDR", podCIDR)
	}

	if err := netlink.NeighDel(&netlink.Neigh{LinkIndex: linkIdx, Family: AF_INET, IP: remoteIP}); err != nil {
		gwmetrics.VTEPRemoveErrorsTotal.Inc()
		v.logger.Error(err, "Warning: failed to remove ARP entry", "nodeIP", nodeIP)
	}

	if err := netlink.NeighDel(&netlink.Neigh{LinkIndex: linkIdx, IP: remoteIP, Family: AF_BRIDGE, Flags: NTF_SELF}); err != nil {
		gwmetrics.VTEPRemoveErrorsTotal.Inc()
		v.logger.Error(err, "Warning: failed to remove FDB entry", "nodeIP", nodeIP)
	}

	v.logger.Info("Remote VTEP removed", "podCIDR", podCIDR, "nodeIP", nodeIP)

	// Metrics: increment VTEP remove counter, decrement node count
	gwmetrics.VTEPRemoveTotal.Inc()
	delete(v.nodes, nodeIP)
	gwmetrics.HybridNodesConfigured.Set(float64(len(v.nodes)))

	return nil
}

// generateMACFromIP creates a unique locally administered MAC from an IP address.
func generateMACFromIP(ip net.IP) net.HardwareAddr {
	ipv4 := ip.To4()
	if ipv4 == nil {
		ipv4 = []byte{0, 0, 0, 0}
	}
	return net.HardwareAddr{0x02, 0x00, ipv4[0], ipv4[1], ipv4[2], ipv4[3]}
}
