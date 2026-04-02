package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/aws/hybrid-gateway/internal/aws"
	"github.com/aws/hybrid-gateway/internal/controller"
	"github.com/aws/hybrid-gateway/internal/gateway"
	"github.com/aws/hybrid-gateway/internal/health"
	gwmetrics "github.com/aws/hybrid-gateway/internal/metrics"
	"github.com/aws/hybrid-gateway/internal/vxlan"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ciliumv2.AddToScheme(scheme))

	// Register custom Prometheus metrics before the metrics server starts
	gwmetrics.Register()
}

func main() {
	var metricsAddr string
	var probeAddr string
	var nodeIP string
	var vpcCIDR string
	var podCIDRs string
	var debug bool
	var leaderElectionID string
	var routeTableIDs string
	var awsRegion string
	var awsInstanceID string
	var leaseDuration time.Duration
	var renewDeadline time.Duration
	var retryPeriod time.Duration

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":10080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8088", "The address the probe endpoint binds to.")
	flag.StringVar(&nodeIP, "node-ip", os.Getenv("NODE_IP"), "Gateway node IP address (env: NODE_IP)")
	flag.StringVar(&vpcCIDR, "vpc-cidr", os.Getenv("VPC_CIDR"), "Cluster VPC CIDR for SNAT destination (env: VPC_CIDR)")
	flag.StringVar(&podCIDRs, "pod-cidrs", os.Getenv("POD_CIDRS"), "Comma-separated hybrid pod CIDRs (env: POD_CIDRS)")
	flag.BoolVar(&debug, "debug", os.Getenv("DEBUG") == "true", "Enable debug logging")
	flag.StringVar(&leaderElectionID, "leader-election-id", getEnvOrDefault("LEADER_ELECTION_ID", "hybrid-gateway-leader"), "Leader election ID (env: LEADER_ELECTION_ID)")
	flag.StringVar(&routeTableIDs, "route-table-ids", os.Getenv("ROUTE_TABLE_IDS"), "Comma-separated list of AWS route table IDs to update (env: ROUTE_TABLE_IDS)")
	flag.StringVar(&awsRegion, "aws-region", os.Getenv("AWS_REGION"), "AWS region (env: AWS_REGION, auto-detected from metadata if not set)")
	flag.StringVar(&awsInstanceID, "aws-instance-id", os.Getenv("AWS_INSTANCE_ID"), "AWS instance ID (env: AWS_INSTANCE_ID, auto-detected from metadata if not set)")
	flag.DurationVar(&leaseDuration, "leader-election-lease-duration", 3*time.Second, "Leader election lease duration")
	flag.DurationVar(&renewDeadline, "leader-election-renew-deadline", 2*time.Second, "Leader election renew deadline")
	flag.DurationVar(&retryPeriod, "leader-election-retry-period", 1*time.Second, "Leader election retry period")
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&zap.Options{Development: debug}))
	ctrl.SetLogger(logger)

	logger.Info("Starting Hybrid Gateway",
		"nodeIP", nodeIP,
		"routeTableIDs", routeTableIDs,
	)

	if nodeIP == "" {
		logger.Error(nil, "NODE_IP is required")
		os.Exit(1)
	}

	localIP := net.ParseIP(nodeIP)
	if localIP == nil {
		logger.Error(nil, "Invalid NODE_IP", "nodeIP", nodeIP)
		os.Exit(1)
	}

	var err error

	vxlanIface := vxlan.NewInterface(vxlan.Config{
		InterfaceName: vxlan.DefaultVXLANInterface,
		VNI:           vxlan.DefaultVNI,
		Port:          vxlan.DefaultPort,
		LocalIP:       localIP,
		Logger:        logger,
	})

	logger.Info("Setting up VXLAN interface")
	if err := vxlanIface.Setup(); err != nil {
		logger.Error(err, "Failed to setup VXLAN")
		os.Exit(1)
	}
	defer func() {
		if err := vxlanIface.Teardown(); err != nil {
			logger.Error(err, "Failed to teardown VXLAN")
		}
	}()

	if podCIDRs == "" || vpcCIDR == "" {
		logger.Error(nil, "pod-cidrs and vpc-cidr are required")
		os.Exit(1)
	}

	podCIDRList := parseCIDRs(podCIDRs)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var routeTableManager *aws.RouteTableManager
	if routeTableIDs != "" {
		if awsRegion == "" {
			awsRegion, err = aws.GetCurrentRegion(ctx)
			if err != nil {
				logger.Error(err, "Failed to auto-detect AWS region")
				os.Exit(1)
			}
		}

		if awsInstanceID == "" {
			awsInstanceID, err = aws.GetCurrentInstanceID(ctx)
			if err != nil {
				logger.Error(err, "Failed to auto-detect AWS instance ID")
				os.Exit(1)
			}
		}

		rtIDs := aws.ParseRouteTableIDs(routeTableIDs)
		if len(rtIDs) == 0 {
			logger.Error(nil, "No valid route table IDs provided")
			os.Exit(1)
		}

		routeTableManager, err = aws.NewRouteTableManager(ctx, rtIDs, awsInstanceID, awsRegion, logger)
		if err != nil {
			logger.Error(err, "Failed to create route table manager")
			os.Exit(1)
		}

		if err := routeTableManager.VerifyRouteTableAccess(ctx); err != nil {
			logger.Error(err, "Failed to verify route table access")
			os.Exit(1)
		}

		logger.Info("Route table manager ready", "routeTables", rtIDs, "region", awsRegion)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         true,
		LeaderElectionID:       leaderElectionID,
		LeaseDuration:          &leaseDuration,
		RenewDeadline:          &renewDeadline,
		RetryPeriod:            &retryPeriod,
	})
	if err != nil {
		logger.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	gatewaySetup := gateway.NewSetup(
		routeTableManager,
		podCIDRList,
		mgr.GetClient(),
		mgr.GetScheme(),
		vxlanIface,
		localIP,
		vpcCIDR,
		logger,
	)
	if err := mgr.Add(gatewaySetup); err != nil {
		logger.Error(err, "Failed to add gateway setup")
		os.Exit(1)
	}

	healthServer := health.NewServer()
	if err := mgr.AddHealthzCheck("healthz", healthServer.HealthCheck); err != nil {
		logger.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthServer.ReadyCheck); err != nil {
		logger.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	vtep := vxlan.NewVTEP(vxlanIface)
	if err = (&controller.NodeReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		VTEP:   vtep,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "Unable to create Node controller")
		os.Exit(1)
	}

	healthServer.SetReady(true)

	// Set gateway info metric with static labels
	gwmetrics.GatewayInfo.WithLabelValues(
		nodeIP,
		os.Getenv("NODE_NAME"),
		vxlan.DefaultVXLANInterface,
		vpcCIDR,
		podCIDRs,
	).Set(1)

	// Register the network stats collector with the Prometheus registry.
	// It implements prometheus.Collector so stats are gathered on-demand
	// during each /metrics scrape – no background goroutine needed.
	statsCollector := gwmetrics.NewNetworkStatsCollector(
		vxlan.DefaultVXLANInterface,
		localIP,
		logger,
	)
	gwmetrics.RegisterNetworkCollector(statsCollector)

	logger.Info("Starting controller manager")

	if err := mgr.Start(ctx); err != nil {
		logger.Error(err, "Problem running manager")
		os.Exit(1)
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// parseCIDRs splits a comma-separated CIDR string into a trimmed slice.
func parseCIDRs(s string) []string {
	var cidrs []string
	for _, c := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(c); trimmed != "" {
			cidrs = append(cidrs, trimmed)
		}
	}
	return cidrs
}
