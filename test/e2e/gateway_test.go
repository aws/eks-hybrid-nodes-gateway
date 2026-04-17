//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/credentials"
	"github.com/aws/eks-hybrid/test/e2e/kubernetes"
	osystem "github.com/aws/eks-hybrid/test/e2e/os"
	"github.com/aws/eks-hybrid/test/e2e/suite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

				cloudNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find cloud node")
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "nginx-cloud", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create nginx pod on cloud node %s", cloudNodeName)

				hybridNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find hybrid node")
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "client-hybrid", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create client pod on hybrid node %s", hybridNodeName)

				test.Logger.Info("Testing cross-VPC pod connectivity (hybrid → cloud)")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-hybrid", "nginx-cloud", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "hybrid → cloud pod connectivity should work")
			})

			It("should route traffic from cloud node pods to hybrid node pods", func(ctx context.Context) {
				testCaseLabels["test-case"] = "pod-cloud-to-hybrid"

				hybridNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find hybrid node")
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-hybrid", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create nginx pod on hybrid node %s", hybridNodeName)

				cloudNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find cloud node")
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
				hybridNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find hybrid node")
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-hybrid-fo", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				cloudNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find cloud node")
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

				newLeader, err := getLeaderPodName(ctx, test)
				Expect(err).NotTo(HaveOccurred(), "should get new leader pod name")
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

		Context("Dynamic Node Lifecycle", func() {
			It("should route traffic to a newly joined hybrid node", func(ctx context.Context) {
				testCaseLabels["test-case"] = "dynamic-node-lifecycle"

				hybridNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find hybrid node")
				cloudNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find cloud node")

				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-baseline", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "client-dynamic", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				test.Logger.Info("Verifying baseline connectivity before node join")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-dynamic", "nginx-baseline", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "baseline connectivity should work")

				newNodeName := fmt.Sprintf("gateway-dynamic-%s", sharedTestData.TestRunID)
				ssmProvider := &credentials.SsmProvider{
					SSM:  test.SSMClient,
					Role: test.StackOut.SSMNodeRoleName,
				}
				newNode := suite.NodeCreate{
					InstanceName: newNodeName,
					InstanceSize: e2e.Large,
					NodeName:     newNodeName,
					OS:           osystem.NewUbuntu2204AMD(),
					Provider:     ssmProvider,
					ComputeType:  e2e.CPUInstance,
				}

				test.Logger.Info("Joining second hybrid node dynamically", "name", newNodeName)
				suite.CreateNodes(ctx, test, []suite.NodeCreate{newNode})

				nodes, err := test.K8sClient.Interface.CoreV1().Nodes().List(ctx, metav1.ListOptions{
					LabelSelector: "eks.amazonaws.com/compute-type=hybrid",
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(nodes.Items).To(HaveLen(2), "expected exactly 2 hybrid nodes (original + dynamic)")
				var dynamicNodeK8sName string
				for _, n := range nodes.Items {
					if n.Name != hybridNodeName {
						dynamicNodeK8sName = n.Name
					}
				}
				Expect(dynamicNodeK8sName).NotTo(BeEmpty(), "should find dynamically joined node")

				DeferCleanup(func(ctx context.Context) {
					if dynamicNodeK8sName != "" {
						_ = test.K8sClient.Interface.CoreV1().Nodes().Delete(ctx, dynamicNodeK8sName, metav1.DeleteOptions{})
					}
				})

				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, dynamicNodeK8sName, "default", test.Cluster.Region, test.Logger, "nginx-dynamic", testCaseLabels)
				Expect(err).NotTo(HaveOccurred(), "should create pod on dynamically joined node")

				test.Logger.Info("Testing connectivity to pod on dynamically joined node")
				Eventually(func(g Gomega) {
					err := kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-dynamic", "nginx-dynamic", "default", test.Logger)
					g.Expect(err).NotTo(HaveOccurred())
				}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "should reach pod on dynamically joined node")

				test.Logger.Info("Verifying original hybrid node not disrupted by join")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-dynamic", "nginx-baseline", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "original hybrid should still be reachable after node join")

				test.Logger.Info("Deleting pods on dynamic node before removal")
				_ = kubernetes.DeletePodsWithLabels(ctx, test.K8sClient.Interface, "default", "test-case=dynamic-node-lifecycle", test.Logger)

				test.Logger.Info("Removing dynamically joined node", "name", newNodeName)
				err = test.K8sClient.Interface.CoreV1().Nodes().Delete(ctx, dynamicNodeK8sName, metav1.DeleteOptions{})
				Expect(err).NotTo(HaveOccurred(), "should delete dynamic node")

				test.Logger.Info("Waiting for node deletion to propagate")
				Eventually(func(g Gomega) {
					_, err := test.K8sClient.Interface.CoreV1().Nodes().Get(ctx, dynamicNodeK8sName, metav1.GetOptions{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "dynamic node should be gone, got: %v", err)
				}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-post-remove", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "client-post-remove", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				test.Logger.Info("Verifying connectivity after dynamic node removal")
				Eventually(func(g Gomega) {
					err := kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-post-remove", "nginx-post-remove", "default", test.Logger)
					g.Expect(err).NotTo(HaveOccurred())
				}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "original hybrid should still work after dynamic node removal")
			})
		})

		Context("Webhook Connectivity", func() {
			It("should support admission webhook calls to pods on hybrid nodes through gateway", func(ctx context.Context) {
				testCaseLabels["test-case"] = "webhook-connectivity"

				test.Logger.Info("Deploying webhook server on hybrid node")
				DeferCleanup(func(ctx context.Context) {
					cleanupWebhookTest(ctx, test)
				})
				caPEM := deployWebhookOnHybridNode(ctx, test)

				test.Logger.Info("Registering validating webhook")
				registerValidatingWebhook(ctx, test, caPEM)

				test.Logger.Info("Triggering webhook by creating a ConfigMap")
				triggerCM := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "webhook-trigger", Namespace: webhookTestNamespace},
				}
				_, err := test.K8sClient.Interface.CoreV1().ConfigMaps(webhookTestNamespace).Create(ctx, triggerCM, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred(), "ConfigMap creation should succeed — proves API server reached webhook on hybrid node through gateway")

				test.Logger.Info("Webhook connectivity verified — API server reached hybrid node through gateway")
			})
		})

		Context("Helm Upgrade", func() {
			It("should maintain connectivity after helm upgrade", func(ctx context.Context) {
				testCaseLabels["test-case"] = "helm-upgrade"

				hybridNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find hybrid node")
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-upgrade", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				cloudNodeName, err := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "should find cloud node")
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "client-upgrade", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				test.Logger.Info("Verifying pre-upgrade connectivity")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-upgrade", "nginx-upgrade", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "pre-upgrade connectivity should work")

				gatewayChart := requireEnv("GATEWAY_CHART")
				gatewayChartVersion := requireEnv("GATEWAY_CHART_VERSION")

				test.Logger.Info("Running helm upgrade with additional pod CIDR")
				// Upgrade adds 10.88.0.0/16 to podCIDRs. We verify the upgrade
				// succeeds and existing connectivity survives; reachability of
				// the new CIDR is not tested right now but would be good to add in the future.
				upgradeGatewayChart(ctx, test, gatewayChart, gatewayChartVersion, sharedTestData.GatewayImageURI, sharedTestData.RouteTableIDs, "10.87.0.0/16,10.88.0.0/16")

				test.Logger.Info("Waiting for upgraded pods to be ready")
				err = kubernetes.WaitForPodsToBeRunning(ctx, test.K8sClient.Interface, metav1.ListOptions{
					LabelSelector: "app.kubernetes.io/name=eks-hybrid-nodes-gateway",
				}, gatewayNamespace, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "upgraded gateway pods should be ready")

				test.Logger.Info("Verifying post-upgrade connectivity")
				Eventually(func(g Gomega) {
					err := kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-upgrade", "nginx-upgrade", "default", test.Logger)
					g.Expect(err).NotTo(HaveOccurred())
				}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "connectivity should work after helm upgrade")
			})
		})
	})
})
