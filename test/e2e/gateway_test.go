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
			It("should recover connectivity after leader pod is deleted", func(ctx context.Context) {
				Skip("temporarily skipped - pending manual validation")
				testCaseLabels["test-case"] = "leader-failover"

				hybridNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, hybridNodeLabelKey, hybridNodeLabelValue, test.Logger)
				err := kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, hybridNodeName, "default", test.Cluster.Region, test.Logger, "nginx-failover", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				cloudNodeName, _ := kubernetes.FindNodeWithLabel(ctx, test.K8sClient.Interface, "node.kubernetes.io/instance-type", cloudInstanceType, test.Logger)
				err = kubernetes.CreateNginxPodInNode(ctx, test.K8sClient.Interface, cloudNodeName, "default", test.Cluster.Region, test.Logger, "client-failover", testCaseLabels)
				Expect(err).NotTo(HaveOccurred())

				test.Logger.Info("Verifying connectivity before failover")
				err = kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-failover", "nginx-failover", "default", test.Logger)
				Expect(err).NotTo(HaveOccurred(), "pre-failover connectivity should work")

				test.Logger.Info("Deleting gateway pods to trigger failover")
				err = kubernetes.DeletePodsWithLabels(ctx, test.K8sClient.Interface, gatewayNamespace, "app.kubernetes.io/name=eks-hybrid-nodes-gateway", test.Logger)
				Expect(err).NotTo(HaveOccurred())

				test.Logger.Info("Waiting for gateway pods to recover")
				err = kubernetes.WaitForPodsToBeRunning(ctx, test.K8sClient.Interface, metav1.ListOptions{
					LabelSelector: "app.kubernetes.io/name=eks-hybrid-nodes-gateway",
				}, gatewayNamespace, test.Logger)
				Expect(err).NotTo(HaveOccurred(), "gateway pods should recover")

				test.Logger.Info("Verifying connectivity after failover")
				Eventually(func(g Gomega) {
					err := kubernetes.TestPodToPodConnectivity(ctx, test.K8sClientConfig, test.K8sClient.Interface, "client-failover", "nginx-failover", "default", test.Logger)
					g.Expect(err).NotTo(HaveOccurred())
				}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "post-failover connectivity should recover")
			})
		})
	})
})
