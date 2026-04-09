//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/eks-hybrid/test/e2e/kubernetes"
	"github.com/aws/eks-hybrid/test/e2e/suite"
	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// probeInterval is the fixed cadence between connectivity probes. This keeps
// exec call rate predictable and makes gap measurements consistent.
const probeInterval = 500 * time.Millisecond

// getLeaderPodName reads the controller-runtime Lease for the gateway's leader
// election and resolves it to a running pod name. It returns an error rather
// than failing via Gomega so callers like Eventually can retry on transient
// states (e.g. lease briefly unowned during failover). Because the gateway
// runs with hostNetwork: true, the holderIdentity is the node hostname rather
// than the pod name, so we match via spec.nodeName.
func getLeaderPodName(ctx context.Context, test *suite.PeeredVPCTest) (string, error) {
	lease, err := test.K8sClient.Interface.CoordinationV1().Leases(gatewayNamespace).Get(
		ctx, "hybrid-gateway-leader", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading lease: %w", err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return "", fmt.Errorf("lease has no holder")
	}

	holderIdentity := *lease.Spec.HolderIdentity

	pods, err := test.K8sClient.Interface.CoreV1().Pods(gatewayNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=eks-hybrid-nodes-gateway",
	})
	if err != nil {
		return "", fmt.Errorf("listing gateway pods: %w", err)
	}

	// controller-runtime encodes holderIdentity as "<hostname>_<uuid>".
	// With hostNetwork the hostname is the node name, not the pod name.
	for i := range pods.Items {
		if strings.HasPrefix(holderIdentity, pods.Items[i].Spec.NodeName) {
			return pods.Items[i].Name, nil
		}
	}

	return "", fmt.Errorf("no running gateway pod matches leader identity %q", holderIdentity)
}

// findLeaderPod resolves the current leader pod name, failing the test
// immediately if the lease cannot be read or matched to a running pod.
func findLeaderPod(ctx context.Context, test *suite.PeeredVPCTest) string {
	name, err := getLeaderPodName(ctx, test)
	Expect(err).NotTo(HaveOccurred(), "should find leader pod")
	return name
}

// pingResult captures the outcome of a single HTTP probe.
type pingResult struct {
	timestamp time.Time
	success   bool
	direction string
}

// connectivityMonitor continuously probes an HTTP endpoint from inside a
// Kubernetes pod and records the result of every attempt. Probes run at a
// fixed interval to keep API server load predictable and make gap
// measurements consistent. It is safe for concurrent use.
type connectivityMonitor struct {
	mu      sync.Mutex
	results []pingResult
	cancel  context.CancelFunc
	done    chan struct{}
}

// startConnectivityMonitor launches a background goroutine that executes
// curl inside clientPod, targeting the given IP on port 80. Each probe uses
// a 1 s connect timeout and 2 s total timeout. The command is wrapped with
// "|| true" so the shell always exits 0, preventing ExecPodWithRetries from
// retrying on curl-level failures; we distinguish success from failure by
// inspecting the HTTP status code written to stdout.
//
// Probes run at a fixed interval (probeInterval) to avoid hammering the API
// server with exec calls and to produce consistent timing measurements.
func startConnectivityMonitor(
	ctx context.Context,
	test *suite.PeeredVPCTest,
	clientPod, targetIP, namespace, direction string,
) *connectivityMonitor {
	monCtx, cancel := context.WithCancel(ctx)
	m := &connectivityMonitor{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	curlCmd := fmt.Sprintf(
		"curl -s -o /dev/null -w '%%{http_code}' http://%s:80 --connect-timeout 1 --max-time 2 || true",
		targetIP)

	go func() {
		defer close(m.done)
		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-monCtx.Done():
				return
			case <-ticker.C:
			}

			stdout, _, err := kubernetes.ExecPodWithRetries(
				monCtx, test.K8sClientConfig, test.K8sClient.Interface,
				clientPod, namespace, "sh", "-c", curlCmd)

			if monCtx.Err() != nil {
				return
			}

			success := err == nil && strings.TrimSpace(stdout) == "200"
			m.mu.Lock()
			m.results = append(m.results, pingResult{
				timestamp: time.Now(),
				success:   success,
				direction: direction,
			})
			m.mu.Unlock()
		}
	}()

	return m
}

// stop signals the monitor goroutine to exit, waits for it to finish, and
// returns a snapshot of all collected results.
func (m *connectivityMonitor) stop() []pingResult {
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]pingResult, len(m.results))
	copy(out, m.results)
	return out
}

// successCount returns the number of successful probes recorded so far.
// It is safe to call while the monitor is still running.
func (m *connectivityMonitor) successCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.results {
		if r.success {
			n++
		}
	}
	return n
}

// analyzeConnectivity computes the maximum elapsed time between two
// consecutive successful probes and logs a summary. It returns an error if
// fewer than 2 successful probes were recorded, since gap calculation
// requires at least two data points and zero successes would otherwise
// silently report zero downtime.
func analyzeConnectivity(results []pingResult, logger logr.Logger) (time.Duration, error) {
	if len(results) == 0 {
		return 0, fmt.Errorf("no connectivity results to analyze")
	}

	total := len(results)
	successCount := 0
	for _, r := range results {
		if r.success {
			successCount++
		}
	}

	if successCount < 2 {
		logger.Info("Connectivity results",
			"direction", results[0].direction,
			"total", total,
			"successes", successCount,
			"successRate", "0.0%",
		)
		return 0, fmt.Errorf("fewer than 2 successful probes (%d/%d); cannot compute gap — connectivity may be fully broken", successCount, total)
	}

	var maxGap time.Duration
	var lastSuccess time.Time
	for _, r := range results {
		if r.success {
			if !lastSuccess.IsZero() {
				if gap := r.timestamp.Sub(lastSuccess); gap > maxGap {
					maxGap = gap
				}
			}
			lastSuccess = r.timestamp
		}
	}

	failures := total - successCount
	logger.Info("Connectivity results",
		"direction", results[0].direction,
		"total", total,
		"failures", failures,
		"successRate", fmt.Sprintf("%.1f%%", float64(successCount)/float64(total)*100),
		"maxGap", maxGap.String(),
	)

	return maxGap, nil
}
