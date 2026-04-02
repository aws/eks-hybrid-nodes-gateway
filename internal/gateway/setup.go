package gateway

import (
	"context"
	"net"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/aws/hybrid-gateway/internal/aws"
	"github.com/aws/hybrid-gateway/internal/cilium"
	gwmetrics "github.com/aws/hybrid-gateway/internal/metrics"
	"github.com/aws/hybrid-gateway/internal/vxlan"
)

// Setup is a leader-elected runnable that handles leader-only actions:
// - Updating AWS route tables to point pod CIDRs to this gateway's primary ENI
// - Upserting the CiliumVTEPConfig CRD so hybrid nodes route via the active leader
//
// VXLAN interface setup and SNAT rules are intentionally NOT managed here;
// they are set up unconditionally at startup in main.go so that every gateway
// node is always ready to forward traffic.
type Setup struct {
	routeTableManager *aws.RouteTableManager
	podCIDRs          []string
	k8sClient         client.Client
	scheme            *runtime.Scheme
	vxlanIface        *vxlan.Interface
	nodeIP            net.IP
	vpcCIDR           string
	logger            logr.Logger
}

func NewSetup(
	rtm *aws.RouteTableManager,
	podCIDRs []string,
	k8sClient client.Client,
	scheme *runtime.Scheme,
	vxlanIface *vxlan.Interface,
	nodeIP net.IP,
	vpcCIDR string,
	logger logr.Logger,
) *Setup {
	return &Setup{
		routeTableManager: rtm,
		podCIDRs:          podCIDRs,
		k8sClient:         k8sClient,
		scheme:            scheme,
		vxlanIface:        vxlanIface,
		nodeIP:            nodeIP,
		vpcCIDR:           vpcCIDR,
		logger:            logger,
	}
}

// Start implements manager.Runnable. Called once when this instance wins the leader lease.
func (g *Setup) Start(ctx context.Context) error {
	setupStart := time.Now()

	// Mark this instance as leader
	gwmetrics.LeaderIsActive.Set(1)

	// Update AWS route tables so traffic for hybrid pod CIDRs routes to this instance
	if g.routeTableManager != nil {
		g.logger.Info("Updating route tables", "podCIDRs", g.podCIDRs)
		rtStart := time.Now()
		if err := g.routeTableManager.UpdateRoutes(ctx, g.podCIDRs); err != nil {
			g.logger.Error(err, "Failed to update route tables")
			gwmetrics.RouteTableUpdateErrorsTotal.Inc()
			return err
		}
		gwmetrics.RouteTableUpdateDuration.Observe(time.Since(rtStart).Seconds())
		gwmetrics.RouteTableUpdateTotal.Inc()
		g.logger.Info("Route tables updated")
	}

	// Upsert CiliumVTEPConfig CRD with the leader's node IP as VTEP endpoint
	if g.vpcCIDR != "" {
		if err := cilium.UpsertCiliumVTEPConfig(
			ctx,
			g.k8sClient,
			g.vxlanIface,
			g.nodeIP,
			g.vpcCIDR,
			g.logger,
		); err != nil {
			g.logger.Error(err, "Failed to upsert CiliumVTEPConfig")
			// Don't return error – route tables may still be functional
		}
	}

	// Record leader setup duration
	gwmetrics.LeaderSetupDuration.Observe(time.Since(setupStart).Seconds())
	g.logger.Info("Leader setup complete")

	<-ctx.Done()

	// No longer leader
	gwmetrics.LeaderIsActive.Set(0)
	g.logger.Info("Leadership ended")
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (g *Setup) NeedLeaderElection() bool { return true }

var (
	_ manager.Runnable               = &Setup{}
	_ manager.LeaderElectionRunnable = &Setup{}
)
