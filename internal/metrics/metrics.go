// Package metrics defines all custom Prometheus metrics emitted by the
// hybrid-gateway. Metrics are registered against the controller-runtime
// metrics registry so they are automatically exposed on the /metrics endpoint.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	namespace = "hybrid_gateway"
)

// ─── Hybrid Node Metrics ────────────────────────────────────────────────────

// HybridNodesConfigured is the current number of hybrid nodes with
// VTEP entries configured in the gateway.
var HybridNodesConfigured = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "hybrid_nodes_configured",
	Help:      "Current number of hybrid nodes with VTEP entries configured.",
})

// ─── VTEP Operation Metrics ─────────────────────────────────────────────────

var (
	// VTEPAddTotal counts successful VTEP add operations.
	VTEPAddTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vtep",
		Name:      "add_total",
		Help:      "Total number of successful VTEP add operations (AddRemoteNode).",
	})

	// VTEPAddErrorsTotal counts failed VTEP add operations.
	VTEPAddErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vtep",
		Name:      "add_errors_total",
		Help:      "Total number of failed VTEP add operations.",
	})

	// VTEPRemoveTotal counts successful VTEP remove operations.
	VTEPRemoveTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vtep",
		Name:      "remove_total",
		Help:      "Total number of successful VTEP remove operations.",
	})

	// VTEPRemoveErrorsTotal counts failed VTEP remove operations.
	VTEPRemoveErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "vtep",
		Name:      "remove_errors_total",
		Help:      "Total number of failed VTEP remove operations.",
	})
)

// ─── AWS Route Table Metrics ────────────────────────────────────────────────

var (
	// RouteTableUpdateTotal counts successful AWS route table updates.
	RouteTableUpdateTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "aws_route_table",
		Name:      "update_total",
		Help:      "Total number of successful AWS route table update operations.",
	})

	// RouteTableUpdateErrorsTotal counts failed AWS route table updates.
	RouteTableUpdateErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "aws_route_table",
		Name:      "update_errors_total",
		Help:      "Total number of failed AWS route table update operations.",
	})

	// RouteTableUpdateDuration tracks the duration of route table updates.
	RouteTableUpdateDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "aws_route_table",
		Name:      "update_duration_seconds",
		Help:      "Duration of AWS route table update operations in seconds.",
		Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
	})
)

// ─── Leader Election / Failover Metrics ─────────────────────────────────────

var (
	// LeaderIsActive indicates whether this instance is the active leader.
	// 1 = leader, 0 = standby. This complements the built-in
	// leader_election_master_status metric with a gateway-namespaced version.
	LeaderIsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "leader_is_active",
		Help:      "1 if this gateway instance is the active leader, 0 if standby.",
	})

	// LeaderSetupDuration tracks how long the leader setup takes
	// (VXLAN + route tables + CiliumVTEPConfig).
	LeaderSetupDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "leader_setup_duration_seconds",
		Help:      "Duration of leader setup operations (route tables + CiliumVTEPConfig) in seconds.",
		Buckets:   []float64{0.5, 1, 2, 5, 10, 15, 30},
	})
)

// ─── Gateway Info Metric ────────────────────────────────────────────────────

// GatewayInfo provides static labels about the gateway instance.
var GatewayInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "info",
	Help:      "Static information about the gateway instance. Always 1.",
}, []string{"node_ip", "node_name", "vxlan_interface", "vpc_cidr", "pod_cidr"})

// Register registers all custom metrics with the controller-runtime metrics registry.
// This must be called during init or startup before the metrics server starts.
func Register() {
	metrics.Registry.MustRegister(
		// Hybrid nodes
		HybridNodesConfigured,
		// VTEP operations
		VTEPAddTotal,
		VTEPAddErrorsTotal,
		VTEPRemoveTotal,
		VTEPRemoveErrorsTotal,
		// AWS route table
		RouteTableUpdateTotal,
		RouteTableUpdateErrorsTotal,
		RouteTableUpdateDuration,
		// Leader / failover
		LeaderIsActive,
		LeaderSetupDuration,
		// Info
		GatewayInfo,
	)
}

// RegisterNetworkCollector registers a NetworkStatsCollector (which implements
// prometheus.Collector) with the controller-runtime metrics registry. The
// collector gathers VXLAN and primary NIC stats on-demand during each scrape.
func RegisterNetworkCollector(c *NetworkStatsCollector) {
	metrics.Registry.MustRegister(c)
}
