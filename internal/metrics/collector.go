package metrics

import (
	"net"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vishvananda/netlink"
)

const (
	// AF_BRIDGE is the address family constant for bridge FDB entries.
	AF_BRIDGE = 7
)

// metric descriptors – created once, reused in Describe() and Collect().
var (
	vxlanRxBytesDesc   = prometheus.NewDesc(fqName("vxlan", "rx_bytes_total"), "Total bytes received on the VXLAN interface (hybrid_vxlan0).", nil, nil)
	vxlanTxBytesDesc   = prometheus.NewDesc(fqName("vxlan", "tx_bytes_total"), "Total bytes transmitted on the VXLAN interface (hybrid_vxlan0).", nil, nil)
	vxlanRxPacketsDesc = prometheus.NewDesc(fqName("vxlan", "rx_packets_total"), "Total packets received on the VXLAN interface.", nil, nil)
	vxlanTxPacketsDesc = prometheus.NewDesc(fqName("vxlan", "tx_packets_total"), "Total packets transmitted on the VXLAN interface.", nil, nil)
	vxlanRxDroppedDesc = prometheus.NewDesc(fqName("vxlan", "rx_dropped_total"), "Total packets dropped on receive by the VXLAN interface.", nil, nil)
	vxlanTxDroppedDesc = prometheus.NewDesc(fqName("vxlan", "tx_dropped_total"), "Total packets dropped on transmit by the VXLAN interface.", nil, nil)
	vxlanRxErrorsDesc  = prometheus.NewDesc(fqName("vxlan", "rx_errors_total"), "Total receive errors on the VXLAN interface.", nil, nil)
	vxlanTxErrorsDesc  = prometheus.NewDesc(fqName("vxlan", "tx_errors_total"), "Total transmit errors on the VXLAN interface.", nil, nil)
	vxlanIfUpDesc      = prometheus.NewDesc(fqName("vxlan", "interface_up"), "1 if the VXLAN interface (hybrid_vxlan0) is UP, 0 otherwise.", nil, nil)
	vxlanFDBDesc       = prometheus.NewDesc(fqName("vxlan", "fdb_entries"), "Current number of FDB (forwarding database) entries on hybrid_vxlan0.", nil, nil)
	vxlanRouteDesc     = prometheus.NewDesc(fqName("vxlan", "route_count"), "Current number of routes via the VXLAN interface.", nil, nil)

	nicRxBytesDesc   = prometheus.NewDesc(fqName("primary_nic", "rx_bytes_total"), "Total bytes received on the primary network interface.", nil, nil)
	nicTxBytesDesc   = prometheus.NewDesc(fqName("primary_nic", "tx_bytes_total"), "Total bytes transmitted on the primary network interface.", nil, nil)
	nicRxPacketsDesc = prometheus.NewDesc(fqName("primary_nic", "rx_packets_total"), "Total packets received on the primary network interface.", nil, nil)
	nicTxPacketsDesc = prometheus.NewDesc(fqName("primary_nic", "tx_packets_total"), "Total packets transmitted on the primary network interface.", nil, nil)
	nicRxDroppedDesc = prometheus.NewDesc(fqName("primary_nic", "rx_dropped_total"), "Total packets dropped on receive by the primary NIC.", nil, nil)
	nicTxDroppedDesc = prometheus.NewDesc(fqName("primary_nic", "tx_dropped_total"), "Total packets dropped on transmit by the primary NIC.", nil, nil)
	nicRxErrorsDesc  = prometheus.NewDesc(fqName("primary_nic", "rx_errors_total"), "Total receive errors on the primary NIC.", nil, nil)
	nicTxErrorsDesc  = prometheus.NewDesc(fqName("primary_nic", "tx_errors_total"), "Total transmit errors on the primary NIC.", nil, nil)
	nicInfoDesc      = prometheus.NewDesc(fqName("primary_nic", "info"), "Primary NIC name and speed info. Always 1.", []string{"interface_name"}, nil)
)

// fqName builds a fully-qualified metric name under the hybrid_gateway namespace.
func fqName(subsystem, name string) string {
	return prometheus.BuildFQName(namespace, subsystem, name)
}

// NetworkStatsCollector implements prometheus.Collector. It reads the kernel's
// interface counters for the VXLAN interface and the primary NIC on demand
// whenever Prometheus scrapes the /metrics endpoint. This avoids the need for
// a background polling goroutine – stats are gathered only when requested.
type NetworkStatsCollector struct {
	vxlanInterface string
	primaryNICName string // detected from NODE_IP
	logger         logr.Logger
}

// NewNetworkStatsCollector creates a new collector for the given VXLAN interface.
// The primary NIC is auto-detected by finding which interface holds the given nodeIP.
func NewNetworkStatsCollector(vxlanInterface string, nodeIP net.IP, logger logr.Logger) *NetworkStatsCollector {
	primaryNIC := detectPrimaryNICByIP(nodeIP, logger)
	logger.Info("Created network stats collector (prometheus.Collector)",
		"vxlanInterface", vxlanInterface,
		"primaryNIC", primaryNIC,
	)
	return &NetworkStatsCollector{
		vxlanInterface: vxlanInterface,
		primaryNICName: primaryNIC,
		logger:         logger.WithName("metrics-collector"),
	}
}

// detectPrimaryNICByIP finds the network interface that has the given IP address
// assigned to it. This uses the NODE_IP (from the downward API) to reliably
// identify the primary interface regardless of routing configuration.
func detectPrimaryNICByIP(nodeIP net.IP, logger logr.Logger) string {
	if nodeIP == nil {
		logger.Info("No NODE_IP provided for primary NIC detection")
		return ""
	}

	links, err := netlink.LinkList()
	if err != nil {
		logger.Error(err, "Failed to list interfaces for primary NIC detection")
		return ""
	}

	for _, link := range links {
		addrs, err := netlink.AddrList(link, 0) // 0 = all address families
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if addr.IP.Equal(nodeIP) {
				name := link.Attrs().Name
				logger.Info("Detected primary NIC from NODE_IP",
					"interface", name, "nodeIP", nodeIP)
				return name
			}
		}
	}

	logger.Info("Could not find interface with NODE_IP, falling back to eth0",
		"nodeIP", nodeIP)
	return "eth0"
}

// Describe implements prometheus.Collector. It sends all metric descriptors
// to the provided channel.
func (c *NetworkStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	// VXLAN interface
	ch <- vxlanRxBytesDesc
	ch <- vxlanTxBytesDesc
	ch <- vxlanRxPacketsDesc
	ch <- vxlanTxPacketsDesc
	ch <- vxlanRxDroppedDesc
	ch <- vxlanTxDroppedDesc
	ch <- vxlanRxErrorsDesc
	ch <- vxlanTxErrorsDesc
	ch <- vxlanIfUpDesc
	ch <- vxlanFDBDesc
	ch <- vxlanRouteDesc
	// Primary NIC
	ch <- nicRxBytesDesc
	ch <- nicTxBytesDesc
	ch <- nicRxPacketsDesc
	ch <- nicTxPacketsDesc
	ch <- nicRxDroppedDesc
	ch <- nicTxDroppedDesc
	ch <- nicRxErrorsDesc
	ch <- nicTxErrorsDesc
	ch <- nicInfoDesc
}

// Collect implements prometheus.Collector. It reads current stats from the
// kernel via netlink and sends metric values to the provided channel. This
// is called on every Prometheus scrape – no background goroutine needed.
func (c *NetworkStatsCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectVXLAN(ch)
	c.collectPrimaryNIC(ch)
}

// collectVXLAN reads stats from the VXLAN interface and sends them to ch.
func (c *NetworkStatsCollector) collectVXLAN(ch chan<- prometheus.Metric) {
	link, err := netlink.LinkByName(c.vxlanInterface)
	if err != nil {
		c.logger.V(1).Info("VXLAN interface not found, reporting down",
			"interface", c.vxlanInterface, "error", err)
		ch <- prometheus.MustNewConstMetric(vxlanIfUpDesc, prometheus.GaugeValue, 0)
		return
	}

	// Interface UP/DOWN state
	attrs := link.Attrs()
	if attrs.OperState == netlink.OperUp || attrs.Flags&net.FlagUp != 0 {
		ch <- prometheus.MustNewConstMetric(vxlanIfUpDesc, prometheus.GaugeValue, 1)
	} else {
		ch <- prometheus.MustNewConstMetric(vxlanIfUpDesc, prometheus.GaugeValue, 0)
	}

	// Interface statistics from kernel
	if stats := attrs.Statistics; stats != nil {
		ch <- prometheus.MustNewConstMetric(vxlanRxBytesDesc, prometheus.CounterValue, float64(stats.RxBytes))
		ch <- prometheus.MustNewConstMetric(vxlanTxBytesDesc, prometheus.CounterValue, float64(stats.TxBytes))
		ch <- prometheus.MustNewConstMetric(vxlanRxPacketsDesc, prometheus.CounterValue, float64(stats.RxPackets))
		ch <- prometheus.MustNewConstMetric(vxlanTxPacketsDesc, prometheus.CounterValue, float64(stats.TxPackets))
		ch <- prometheus.MustNewConstMetric(vxlanRxDroppedDesc, prometheus.CounterValue, float64(stats.RxDropped))
		ch <- prometheus.MustNewConstMetric(vxlanTxDroppedDesc, prometheus.CounterValue, float64(stats.TxDropped))
		ch <- prometheus.MustNewConstMetric(vxlanRxErrorsDesc, prometheus.CounterValue, float64(stats.RxErrors))
		ch <- prometheus.MustNewConstMetric(vxlanTxErrorsDesc, prometheus.CounterValue, float64(stats.TxErrors))
	}

	// Count FDB entries
	fdbEntries, err := netlink.NeighList(attrs.Index, AF_BRIDGE)
	if err == nil {
		ch <- prometheus.MustNewConstMetric(vxlanFDBDesc, prometheus.GaugeValue, float64(len(fdbEntries)))
	}

	// Count routes via this interface
	routes, err := netlink.RouteList(link, 0)
	if err == nil {
		ch <- prometheus.MustNewConstMetric(vxlanRouteDesc, prometheus.GaugeValue, float64(len(routes)))
	}
}

// collectPrimaryNIC reads stats from the primary NIC and sends them to ch.
func (c *NetworkStatsCollector) collectPrimaryNIC(ch chan<- prometheus.Metric) {
	if c.primaryNICName == "" {
		return
	}

	// Always emit the info metric so the NIC name is visible
	ch <- prometheus.MustNewConstMetric(nicInfoDesc, prometheus.GaugeValue, 1, c.primaryNICName)

	link, err := netlink.LinkByName(c.primaryNICName)
	if err != nil {
		c.logger.V(1).Info("Primary NIC not found", "interface", c.primaryNICName, "error", err)
		return
	}
	if stats := link.Attrs().Statistics; stats != nil {
		ch <- prometheus.MustNewConstMetric(nicRxBytesDesc, prometheus.CounterValue, float64(stats.RxBytes))
		ch <- prometheus.MustNewConstMetric(nicTxBytesDesc, prometheus.CounterValue, float64(stats.TxBytes))
		ch <- prometheus.MustNewConstMetric(nicRxPacketsDesc, prometheus.CounterValue, float64(stats.RxPackets))
		ch <- prometheus.MustNewConstMetric(nicTxPacketsDesc, prometheus.CounterValue, float64(stats.TxPackets))
		ch <- prometheus.MustNewConstMetric(nicRxDroppedDesc, prometheus.CounterValue, float64(stats.RxDropped))
		ch <- prometheus.MustNewConstMetric(nicTxDroppedDesc, prometheus.CounterValue, float64(stats.TxDropped))
		ch <- prometheus.MustNewConstMetric(nicRxErrorsDesc, prometheus.CounterValue, float64(stats.RxErrors))
		ch <- prometheus.MustNewConstMetric(nicTxErrorsDesc, prometheus.CounterValue, float64(stats.TxErrors))
	}
}
