//go:build e2e

package e2e

import (
	"context"
	"maps"
	"time"

	"github.com/aws/eks-hybrid/test/e2e/kubernetes"
	"github.com/aws/eks-hybrid/test/e2e/suite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("EKS Hybrid Nodes Gateway", func() {
	When("gateway is deployed with hybrid and cloud nodes", func() {
		var test *suite.PeeredVPCTest
		var testCaseLabels map[string]string

		BeforeEach(func(ctx context.Context) {
			test = suite.BeforeVPCTest(ctx, &sharedTestData.SuiteConfig)
			testCaseLabels = maps.Clone(sharedTestData.GatewayLabels)
		})

		AfterEach(func(ctx context.Context) {
			if testCaseLabels != nil {
				test.Logger.Info("Cleaning up test resources")
				cleanupTestResources(ctx, test, testCaseLabels)
			}
		})

		Context("Pod-to-Pod Communication", func() {
			It("should route traffic from hybrid node pods to cloud node pods", func(ctx context.Context) {
				testCaseLabels["test-case"] = "pod-hybrid-to-cloud"

				cloudNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				err := kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "nginx-cloud", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create nginx pod on cloud node %s", cloudNodeName)

				hybridNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "client-hybrid", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create client pod on hybrid node %s", hybridNodeName)

				test.Logger.Info("Testing cross-VPC pod connectivity (hybrid → cloud)")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-hybrid", "nginx-cloud", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "hybrid → cloud pod connectivity should work")
			})

			It("should route traffic from cloud node pods to hybrid node pods", func(ctx context.Context) {
				testCaseLabels["test-case"] = "pod-cloud-to-hybrid"

				hybridNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				err := kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-hybrid", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create nginx pod on hybrid node %s", hybridNodeName)

				cloudNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "client-cloud", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create client pod on cloud node %s", cloudNodeName)

				test.Logger.Info("Testing cross-VPC pod connectivity (cloud → hybrid)")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-cloud", "nginx-hybrid", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "cloud → hybrid pod connectivity should work")
			})
		})

		Context("Cross-VPC Service Discovery", func() {
			It("should resolve and reach services on cloud nodes from hybrid nodes", func(ctx context.Context) {
				Skip("temporarily skipped - pending manual validation")
				testCaseLabels["test-case"] = "service-hybrid-to-cloud"

				cloudSelector := map[string]string{"node.kubernetes.io/instance-type": cloudInstanceType}
				_, err := kubernetes.CreateDeployment(ctx, test.K8sClient.Interface, "nginx-svc-cloud", "default", test.Cluster.Region, cloudSelector, httpPort, 1, test.Logger, testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create cloud deployment")

				svc, err := kubernetes.CreateService(ctx, test.K8sClient.Interface, "nginx-svc-cloud", "default", map[string]string{"app": "nginx-svc-cloud"}, httpPort, httpPort, test.Logger, testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create cloud service")

				hybridNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "client-hybrid-svc", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create client pod on hybrid node")

				err = kubernetes.WaitForServiceReady(ctx, test.K8sClient.Interface, svc.Name, "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "service should become ready")

				test.Logger.Info("Waiting for cross-VPC DNS propagation")
				time.Sleep(crossVPCPropagationWait)

				err = kubernetes.TestServiceConnectivityWithRetries(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-hybrid-svc", svc.Name, "default", httpPort, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "hybrid → cloud service connectivity should work")
			})

			It("should resolve and reach services on hybrid nodes from cloud nodes", func(ctx context.Context) {
				Skip("temporarily skipped - pending manual validation")
				testCaseLabels["test-case"] = "service-cloud-to-hybrid"

				hybridSelector := map[string]string{"eks.amazonaws.com/compute-type": "hybrid"}
				_, err := kubernetes.CreateDeployment(ctx, test.K8sClient.Interface, "nginx-svc-hybrid", "default", test.Cluster.Region, hybridSelector, httpPort, 1, test.Logger, testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create hybrid deployment")

				svc, err := kubernetes.CreateService(ctx, test.K8sClient.Interface, "nginx-svc-hybrid", "default", map[string]string{"app": "nginx-svc-hybrid"}, httpPort, httpPort, test.Logger, testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create hybrid service")

				cloudNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "client-cloud-svc", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create client pod on cloud node")

				err = kubernetes.WaitForServiceReady(ctx, test.K8sClient.Interface, svc.Name, "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "service should become ready")

				test.Logger.Info("Waiting for cross-VPC DNS propagation")
				time.Sleep(crossVPCPropagationWait)

				err = kubernetes.TestServiceConnectivityWithRetries(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-cloud-svc", svc.Name, "default", httpPort, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "cloud → hybrid service connectivity should work")
			})
		})

		Context("Gateway Resilience", func() {
			It("should maintain connectivity during leader failover", func(ctx context.Context) {
				testCaseLabels["test-case"] = "leader-failover"

				// 1. Deploy test pods — one on each side for bidirectional monitoring.
				hybridNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				err := kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-hybrid-fo", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				cloudNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "nginx-cloud-fo", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				// 2. Verify baseline connectivity in both directions before starting monitors.
				test.Logger.Info("Verifying baseline connectivity (cloud → hybrid)")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "nginx-cloud-fo", "nginx-hybrid-fo", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "baseline cloud → hybrid should work")

				test.Logger.Info("Verifying baseline connectivity (hybrid → cloud)")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "nginx-hybrid-fo", "nginx-cloud-fo", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "baseline hybrid → cloud should work")

				// 3. Resolve pod IPs for the continuous monitors.
				hybridPod, err := test.K8sClient.Interface.CoreV1().Pods("default").Get(ctx, "nginx-hybrid-fo", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				hybridPodIP := hybridPod.Status.PodIP

				cloudPod, err := test.K8sClient.Interface.CoreV1().Pods("default").Get(ctx, "nginx-cloud-fo", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				cloudPodIP := cloudPod.Status.PodIP

				// 4. Identify the current leader via the Lease object.
				leaderPodName := findLeaderPod(ctx, test)
				test.Logger.Info("Identified leader pod", "name", leaderPodName)

				// 5. Start continuous connectivity monitors in both directions.
				test.Logger.Info("Starting continuous connectivity monitoring")
				c2h := startConnectivityMonitor(ctx, test, "nginx-cloud-fo", hybridPodIP, "default", "cloud-to-hybrid")
				h2c := startConnectivityMonitor(ctx, test, "nginx-hybrid-fo", cloudPodIP, "default", "hybrid-to-cloud")

				// 5a. Wait for monitors to accumulate baseline successes. This
				//     catches broken exec/curl setups before triggering failover
				//     so the test fails with a clear message instead of silently
				//     passing with zero probes.
				minBaselineSuccesses := 5
				Eventually(func() int { return c2h.successCount() }).
					WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeNumerically(">=", minBaselineSuccesses), "cloud-to-hybrid monitor should record baseline successes")
				Eventually(func() int { return h2c.successCount() }).
					WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeNumerically(">=", minBaselineSuccesses), "hybrid-to-cloud monitor should record baseline successes")
				test.Logger.Info("Baseline connectivity confirmed", "minSuccesses", minBaselineSuccesses)

				// 6. Delete only the leader pod — the standby should acquire the
				//    lease, re-program route tables and VTEP config, and resume
				//    forwarding traffic with minimal interruption.
				test.Logger.Info("Deleting leader pod to trigger failover", "leader", leaderPodName)
				err = test.K8sClient.Interface.CoreV1().Pods(gatewayNamespace).Delete(ctx, leaderPodName, metav1.DeleteOptions{})
				Expect(err).NotTo(HaveOccurred(), "should delete leader pod")

				// 7. Wait for all gateway pods to be running again.
				test.Logger.Info("Waiting for gateway pods to recover")
				err = kubernetes.WaitForPodsToBeRunning(ctx, test.K8sClient.Interface, metav1.ListOptions{
					LabelSelector: "app.kubernetes.io/name=eks-hybrid-nodes-gateway",
				}, gatewayNamespace, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "gateway pods should recover")

				// 8. Verify a different pod now holds the lease.
				Eventually(func() (string, error) {
					return getLeaderPodName(ctx, test)
				}).WithTimeout(30*time.Second).WithPolling(2*time.Second).ShouldNot(
					Equal(leaderPodName), "a new leader should be elected after deleting %s", leaderPodName)

				newLeader, _ := getLeaderPodName(ctx, test)
				test.Logger.Info("New leader elected", "name", newLeader, "previousLeader", leaderPodName)

				// Let post-failover pings accumulate to confirm stable recovery.
				time.Sleep(30 * time.Second)

				// 9. Stop monitors and analyse results.
				c2hResults := c2h.stop()
				h2cResults := h2c.stop()

				test.Logger.Info("=== Failover Connectivity Analysis ===")
				c2hMaxGap, err := analyzeConnectivity(c2hResults, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "cloud-to-hybrid analysis should have enough data points")
				h2cMaxGap, err2 := analyzeConnectivity(h2cResults, test.Logger)
				Expect(err2).NotTo(HaveOccurred(), "hybrid-to-cloud analysis should have enough data points")

				// 10. Assert the maximum gap between two successful pings never
				//     exceeded the acceptable failover threshold.
				maxAcceptableGap := 30 * time.Second
				Expect(c2hMaxGap).To(BeNumerically("<=", maxAcceptableGap),
					"cloud-to-hybrid max gap should be ≤%s (was %s)", maxAcceptableGap, c2hMaxGap)
				Expect(h2cMaxGap).To(BeNumerically("<=", maxAcceptableGap),
					"hybrid-to-cloud max gap should be ≤%s (was %s)", maxAcceptableGap, h2cMaxGap)
			})
		})
	})
})
