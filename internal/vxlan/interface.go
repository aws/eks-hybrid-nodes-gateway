package vxlan

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"
)

const (
	IPForwardingPath      = "/proc/sys/net/ipv4/ip_forward"
	DefaultVXLANInterface = "hybrid_vxlan0"
	DefaultVNI            = 2
	DefaultPort           = 8472 // Cilium default VXLAN port
)

type Config struct {
	InterfaceName string
	VNI           int
	Port          int
	LocalIP       net.IP
	Logger        logr.Logger
}

type Interface struct {
	config Config
	link   netlink.Link
	mac    string
	logger logr.Logger
}

func NewInterface(config Config) *Interface {
	return &Interface{config: config, logger: config.Logger}
}

// NewInterfaceWithMAC creates an Interface with a preset MAC address.
// Intended for testing.
func NewInterfaceWithMAC(mac string) *Interface {
	return &Interface{mac: mac}
}

// Setup creates and configures the VXLAN interface
func (i *Interface) Setup() error {
	if err := CheckIPForwarding(IPForwardingPath); err != nil {
		return err
	}

	i.logger.Info("Setting up VXLAN interface",
		"name", i.config.InterfaceName,
		"vni", i.config.VNI,
		"port", i.config.Port,
		"localIP", i.config.LocalIP,
	)

	// Delete existing interface if present
	if existing, err := netlink.LinkByName(i.config.InterfaceName); err == nil {
		i.logger.Info("VXLAN interface already exists, deleting", "name", i.config.InterfaceName)
		if err := netlink.LinkDel(existing); err != nil {
			return fmt.Errorf("failed to delete existing interface: %v", err)
		}
	}

	// Generate deterministic MAC from node IP
	macAddr := generateDeterministicMAC(i.config.LocalIP)
	i.logger.Info("Using deterministic MAC", "mac", macAddr)

	vxlanConfig := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name: i.config.InterfaceName,
		},
		VxlanId:  i.config.VNI,
		Port:     i.config.Port,
		Learning: false,
		SrcAddr:  i.config.LocalIP,
	}

	if err := netlink.LinkAdd(vxlanConfig); err != nil {
		return fmt.Errorf("failed to create VXLAN interface: %v", err)
	}

	link, err := netlink.LinkByName(i.config.InterfaceName)
	if err != nil {
		return fmt.Errorf("failed to get VXLAN link: %v", err)
	}

	if err := netlink.LinkSetHardwareAddr(link, macAddr); err != nil {
		return fmt.Errorf("failed to set MAC address: %v", err)
	}

	i.link = link
	i.mac = macAddr.String()

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring up VXLAN interface: %v", err)
	}

	// Note: We intentionally do NOT assign an IP to the VXLAN interface.
	// Cilium VTEP only requires the VXLAN interface's MAC (used as inner dest MAC)
	// and the gateway's physical node IP as the outer tunnel endpoint. Assigning
	// an IP would create an unnecessary connected route (e.g. 192.168.0.0/25).

	i.logger.Info("VXLAN interface ready",
		"name", i.config.InterfaceName,
		"mac", i.mac,
	)
	return nil
}

// Teardown removes the VXLAN interface
func (i *Interface) Teardown() error {
	if i.link == nil {
		return nil
	}
	i.logger.Info("Tearing down VXLAN interface", "name", i.config.InterfaceName)
	if err := netlink.LinkDel(i.link); err != nil {
		return fmt.Errorf("failed to delete VXLAN interface: %v", err)
	}
	return nil
}

// CheckIPForwarding reads the IP forwarding sysctl and returns an error if it is not enabled.
// This is a read-only check; kubelet is responsible for enabling IP forwarding.
func CheckIPForwarding(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read IP forwarding setting at %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		return fmt.Errorf("IP forwarding is not enabled at %s, kubelet should enable this", path)
	}
	return nil
}

func (i *Interface) GetLink() netlink.Link { return i.link }
func (i *Interface) GetMac() string        { return i.mac }

// generateDeterministicMAC creates a locally administered MAC address from an IP
func generateDeterministicMAC(ip net.IP) net.HardwareAddr {
	hash := sha256.Sum256(ip)
	mac := net.HardwareAddr(hash[:6])
	mac[0] = (mac[0] & 0xFE) | 0x02 // locally administered unicast
	return mac
}
