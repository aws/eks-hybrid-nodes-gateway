//go:build e2e

package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2v2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/constants"
	"github.com/aws/eks-hybrid/test/e2e/credentials"
	"github.com/aws/eks-hybrid/test/e2e/kubernetes"
	osystem "github.com/aws/eks-hybrid/test/e2e/os"
	"github.com/aws/eks-hybrid/test/e2e/suite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const gatewayPolicyName = "eks-hybrid-gateway-e2e"

const (
	gatewayNamespace          = "eks-hybrid-nodes-gateway"
	gatewayReleaseName        = "eks-hybrid-nodes-gateway"
	gatewayNodeLabel          = "hybrid-gateway-node"
	vxlanPort                 = 8472
	cloudInstanceType         = "t3.medium"
	gatewayInstanceType       = "t3.large"
	httpPort            int32 = 80

	crossVPCPropagationWait = 180 * time.Second
)

// Cilium 1.19.0 template pre-rendered with our custom values (VTEP, l7proxy, etc.).
//
//go:embed testdata/cilium-template-1.19.0.yaml
var ciliumTemplate []byte

var (
	filePath       string
	sharedTestData SharedTestData

	hybridNodeLabelKey   = "eks.amazonaws.com/compute-type"
	hybridNodeLabelValue = "hybrid"
)

type SharedTestData struct {
	SuiteConfig     suite.SuiteConfiguration `yaml:"suiteConfig"`
	GatewayLabels   map[string]string        `yaml:"gatewayLabels"`
	HybridNodeName  string                   `yaml:"hybridNodeName"`
	TestRunID       string                   `yaml:"testRunId"`
	VpcCIDR         string                   `yaml:"vpcCidr"`
	PodCIDR         string                   `yaml:"podCidr"`
	GatewayImageURI string                   `yaml:"gatewayImageUri"`
	RouteTableIDs   string                   `yaml:"routeTableIds"`
}

func init() {
	flag.StringVar(&filePath, "filepath", "", "Path to e2e configuration file")
}

func TestGatewayE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "EKS Hybrid Nodes Gateway E2E Suite")
}

var _ = SynchronizedBeforeSuite(
	func(ctx context.Context) []byte {
		suiteConfig := suite.BeforeSuiteCredentialSetup(ctx, filePath)
		test := suite.BeforeVPCTest(ctx, &suiteConfig)

		nodeOS := osystem.NewUbuntu2204AMD()
		ssmProvider := &credentials.SsmProvider{
			SSM:  test.SSMClient,
			Role: test.StackOut.SSMNodeRoleName,
		}

		timestamp := time.Now().Format("20060102-150405")
		version := strings.ReplaceAll(test.Cluster.KubernetesVersion, ".", "")
		testRunID := fmt.Sprintf("k8s%s-%s", version, timestamp)
		hybridNodeName := fmt.Sprintf("gateway-hybrid-%s", testRunID)

		gatewayLabels := map[string]string{
			"test-suite":         "gateway",
			"kubernetes-version": test.Cluster.KubernetesVersion,
		}

		test.Logger.Info("Cleaning up resources from previous runs")
		cleanupTestResources(ctx, test, gatewayLabels)

		// 1. Create 1 hybrid node (on-prem side).
		hybridNode := suite.NodeCreate{
			InstanceName: hybridNodeName,
			InstanceSize: e2e.Large,
			NodeName:     hybridNodeName,
			OS:           nodeOS,
			Provider:     ssmProvider,
			ComputeType:  e2e.CPUInstance,
		}
		test.Logger.Info("Creating hybrid node", "name", hybridNodeName)
		suite.CreateNodes(ctx, test, []suite.NodeCreate{hybridNode})

		// 1b. Upgrade Cilium to 1.19.0 with VTEP support enabled.
		test.Logger.Info("Upgrading Cilium to 1.19.0 with VTEP support")
		upgradeCilium(ctx, test)

		// 2. Create 1-node cloud MNG (for cloud-side test pods).
		cloudMNGName := fmt.Sprintf("gateway-cloud-%s", testRunID)
		test.Logger.Info("Creating cloud MNG", "name", cloudMNGName)
		createMNG(ctx, test, cloudMNGName, 1, cloudInstanceType, nil)

		// 3. Create 2-node gateway MNG (labeled for gateway scheduling).
		gatewayMNGName := fmt.Sprintf("gateway-nodes-%s", testRunID)
		gatewayMNGLabels := map[string]string{gatewayNodeLabel: "true"}
		test.Logger.Info("Creating gateway MNG", "name", gatewayMNGName)
		createMNG(ctx, test, gatewayMNGName, 2, gatewayInstanceType, gatewayMNGLabels)

		// 4. Disable source/destination check on gateway MNG instances.
		test.Logger.Info("Disabling source/destination check on gateway nodes")
		disableSourceDestCheck(ctx, test, gatewayMNGLabels)

		// 4b. Allow VXLAN (UDP 8472) ingress on the cluster security group.
		clusterSG := getClusterSecurityGroup(ctx, test)
		test.Logger.Info("Allowing VXLAN ingress on cluster security group", "sgID", clusterSG)
		allowVXLANIngress(ctx, test, clusterSG)

		// 4c. Allow VXLAN ingress on the hybrid node security group (return path).
		hybridSG := test.Cluster.SecurityGroupID
		test.Logger.Info("Allowing VXLAN ingress on hybrid node security group", "sgID", hybridSG)
		allowVXLANIngress(ctx, test, hybridSG)

		// 5. Attach EC2 route table permissions to the MNG node role.
		test.Logger.Info("Attaching gateway IAM policy to MNG role")
		attachGatewayPolicy(ctx, test)

		// 6. Look up the route table(s) for the cluster subnets.
		routeTableIDs := getRouteTableIDs(ctx, test)
		test.Logger.Info("Resolved route tables for cluster subnets", "routeTableIDs", routeTableIDs)
		Expect(routeTableIDs).NotTo(BeEmpty(), "should find at least one route table for cluster subnets")

		// 7. Install gateway chart from OCI registry.
		gatewayImageURI := requireEnv("GATEWAY_IMAGE")
		gatewayChart := requireEnv("GATEWAY_CHART")
		gatewayChartVersion := requireEnv("GATEWAY_CHART_VERSION")

		test.Logger.Info("Installing gateway from OCI registry",
			"chart", gatewayChart,
			"version", gatewayChartVersion,
			"image", gatewayImageURI,
		)
		installGatewayChart(ctx, test, gatewayChart, gatewayChartVersion, gatewayImageURI, routeTableIDs)

		// 8. Wait for gateway pods to be Running and Ready.
		test.Logger.Info("Waiting for gateway pods to be ready")
		err := kubernetes.WaitForPodsToBeRunning(ctx, test.K8sClient.Interface, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=eks-hybrid-nodes-gateway",
		}, gatewayNamespace, test.Logger)
		Expect(err).NotTo(HaveOccurred(), "gateway pods should become ready")

		shared := SharedTestData{
			SuiteConfig:     suiteConfig,
			GatewayLabels:   gatewayLabels,
			HybridNodeName:  hybridNodeName,
			TestRunID:       testRunID,
			VpcCIDR:         "10.20.0.0/16",
			PodCIDR:         "10.87.0.0/16",
			GatewayImageURI: gatewayImageURI,
			RouteTableIDs:   routeTableIDs,
		}

		data, err := yaml.Marshal(shared)
		Expect(err).NotTo(HaveOccurred())
		return data
	},
	func(ctx context.Context, data []byte) {
		err := yaml.Unmarshal(data, &sharedTestData)
		Expect(err).NotTo(HaveOccurred())
	},
)

// createMNG creates an EKS managed node group with the given size and optional Kubernetes labels.
func createMNG(ctx context.Context, test *suite.PeeredVPCTest, name string, desiredSize int32, instanceType string, k8sLabels map[string]string) {
	subnets, err := test.EC2Client.DescribeSubnets(ctx, &ec2v2.DescribeSubnetsInput{
		SubnetIds: test.Cluster.SubnetIds,
		Filters: []ec2v2types.Filter{{
			Name: aws.String("map-public-ip-on-launch"), Values: []string{"true"},
		}},
	})
	Expect(err).NotTo(HaveOccurred(), "should find public subnets")

	var subnetIDs []string
	for _, s := range subnets.Subnets {
		subnetIDs = append(subnetIDs, *s.SubnetId)
	}
	Expect(subnetIDs).NotTo(BeEmpty(), "should have at least one public subnet")

	input := &eks.CreateNodegroupInput{
		ClusterName:   aws.String(test.Cluster.Name),
		NodegroupName: aws.String(name),
		Subnets:       subnetIDs,
		NodeRole:      aws.String(test.StackOut.ManagedNodeRoleArn),
		InstanceTypes: []string{instanceType},
		AmiType:       ekstypes.AMITypesAl2023X8664Standard,
		ScalingConfig: &ekstypes.NodegroupScalingConfig{
			DesiredSize: aws.Int32(desiredSize),
			MaxSize:     aws.Int32(desiredSize),
			MinSize:     aws.Int32(desiredSize),
		},
		Tags: map[string]string{
			constants.TestClusterTagKey: test.Cluster.Name,
			"Name":                      name,
		},
	}
	if len(k8sLabels) > 0 {
		input.Labels = k8sLabels
	}

	_, err = test.EKSClient.CreateNodegroup(ctx, input)
	Expect(err).NotTo(HaveOccurred(), "should create MNG %s", name)

	test.Logger.Info("Waiting for MNG to become active", "name", name)
	waiter := eks.NewNodegroupActiveWaiter(test.EKSClient)
	err = waiter.Wait(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(test.Cluster.Name),
		NodegroupName: aws.String(name),
	}, 15*time.Minute)
	Expect(err).NotTo(HaveOccurred(), "MNG %s should become active", name)
	test.Logger.Info("MNG is active", "name", name, "size", desiredSize)

	DeferCleanup(func(ctx context.Context) {
		if test.SkipCleanup {
			return
		}
		test.Logger.Info("Deleting MNG", "name", name)
		_, _ = test.EKSClient.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{
			ClusterName:   aws.String(test.Cluster.Name),
			NodegroupName: aws.String(name),
		})
	})
}

// disableSourceDestCheck disables source/destination check on the primary ENI
// of EC2 instances backing nodes with the given Kubernetes labels. Required for
// gateway nodes because they forward VXLAN-encapsulated traffic.
// We target the primary ENI (DeviceIndex 0) because ModifyInstanceAttribute
// does not work on instances with multiple network interfaces.
func disableSourceDestCheck(ctx context.Context, test *suite.PeeredVPCTest, k8sLabels map[string]string) {
	selector := labelsToSelector(k8sLabels)
	nodes, err := test.K8sClient.Interface.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	Expect(err).NotTo(HaveOccurred())

	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		instanceID := extractInstanceID(providerID)
		Expect(instanceID).NotTo(BeEmpty(), "should extract instance ID from providerID %s", providerID)

		desc, err := test.EC2Client.DescribeInstances(ctx, &ec2v2.DescribeInstancesInput{
			InstanceIds: []string{instanceID},
		})
		Expect(err).NotTo(HaveOccurred(), "should describe instance %s", instanceID)
		Expect(desc.Reservations).NotTo(BeEmpty())

		var primaryENI string
		for _, eni := range desc.Reservations[0].Instances[0].NetworkInterfaces {
			if eni.Attachment != nil && aws.ToInt32(eni.Attachment.DeviceIndex) == 0 {
				primaryENI = aws.ToString(eni.NetworkInterfaceId)
				break
			}
		}
		Expect(primaryENI).NotTo(BeEmpty(), "should find primary ENI for instance %s", instanceID)

		_, err = test.EC2Client.ModifyNetworkInterfaceAttribute(ctx, &ec2v2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: aws.String(primaryENI),
			SourceDestCheck:    &ec2v2types.AttributeBooleanValue{Value: aws.Bool(false)},
		})
		Expect(err).NotTo(HaveOccurred(), "should disable source/dest check on ENI %s (instance %s)", primaryENI, instanceID)
		test.Logger.Info("Disabled source/destination check", "node", node.Name, "instance", instanceID, "eni", primaryENI)
	}
}

// extractInstanceID extracts the EC2 instance ID from a Kubernetes providerID.
// Format: aws:///us-west-2a/i-0abcdef1234567890
func extractInstanceID(providerID string) string {
	parts := strings.Split(providerID, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// installGatewayChart pulls the chart from an OCI registry and installs it.
func installGatewayChart(ctx context.Context, test *suite.PeeredVPCTest, chartURI, chartVersion, imageURI, routeTableIDs string) {
	settings := cli.New()
	settings.KubeConfig = fmt.Sprintf("/tmp/%s.kubeconfig", test.Cluster.Name)
	cfg := new(action.Configuration)
	err := cfg.Init(settings.RESTClientGetter(), gatewayNamespace, "secret", func(format string, v ...interface{}) {
		test.Logger.Info(fmt.Sprintf(format, v...))
	})
	Expect(err).NotTo(HaveOccurred())

	regClient, err := registry.NewClient()
	Expect(err).NotTo(HaveOccurred(), "should create registry client")

	ecrHost := extractECRHost(chartURI)
	username, password := getECRCredentials(ctx, test.AWS, ecrHost)
	err = regClient.Login(ecrHost, registry.LoginOptBasicAuth(username, password))
	Expect(err).NotTo(HaveOccurred(), "should login to ECR registry %s", ecrHost)

	cfg.RegistryClient = regClient

	install := action.NewInstall(cfg)
	install.Namespace = gatewayNamespace
	install.ReleaseName = gatewayReleaseName
	install.CreateNamespace = true
	install.Wait = true
	install.Timeout = 5 * time.Minute
	install.Version = chartVersion
	install.TakeOwnership = true

	chartPath, err := install.LocateChart(chartURI, settings)
	Expect(err).NotTo(HaveOccurred(), "should locate chart from OCI registry: %s:%s", chartURI, chartVersion)

	chart, err := loader.Load(chartPath)
	Expect(err).NotTo(HaveOccurred(), "should load chart from %s", chartPath)

	repo, tag := splitImage(imageURI)
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"repository": repo,
			"tag":        tag,
		},
		"vpcCIDR":         "10.20.0.0/16",
		"podCIDRs":        "10.87.0.0/16",
		"routeTableIDs":   routeTableIDs,
		"createNamespace": false,
		"autoMode": map[string]interface{}{
			"enabled": false,
		},
	}

	_, err = install.RunWithContext(ctx, chart, vals)
	Expect(err).NotTo(HaveOccurred(), "helm install should succeed")
}

// extractECRHost extracts the ECR hostname from an OCI URI.
func extractECRHost(ociURI string) string {
	host := strings.TrimPrefix(ociURI, "oci://")
	if idx := strings.Index(host, "/"); idx > 0 {
		host = host[:idx]
	}
	return host
}

// getECRCredentials fetches an ECR auth token and returns username/password.
func getECRCredentials(ctx context.Context, awsCfg aws.Config, ecrHost string) (string, string) {
	ecrClient := ecr.NewFromConfig(awsCfg)
	output, err := ecrClient.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	Expect(err).NotTo(HaveOccurred(), "should get ECR auth token for %s", ecrHost)
	Expect(output.AuthorizationData).NotTo(BeEmpty(), "ECR auth data should not be empty")

	decoded, err := base64.StdEncoding.DecodeString(*output.AuthorizationData[0].AuthorizationToken)
	Expect(err).NotTo(HaveOccurred(), "should decode ECR auth token")

	parts := strings.SplitN(string(decoded), ":", 2)
	Expect(parts).To(HaveLen(2), "ECR auth token should be username:password")
	return parts[0], parts[1]
}

func requireEnv(key string) string {
	val := os.Getenv(key)
	Expect(val).NotTo(BeEmpty(), "%s environment variable must be set", key)
	return val
}

func splitImage(uri string) (string, string) {
	i := strings.LastIndex(uri, ":")
	if i < 0 {
		return uri, "latest"
	}
	return uri[:i], uri[i+1:]
}

func cleanupTestResources(ctx context.Context, test *suite.PeeredVPCTest, labels map[string]string) {
	selector := labelsToSelector(labels)
	namespaces := []string{"default", gatewayNamespace}
	for _, ns := range namespaces {
		_ = kubernetes.DeletePodsWithLabels(ctx, test.K8sClient.Interface, ns, selector, test.Logger)
		_ = kubernetes.DeleteServicesWithLabels(ctx, test.K8sClient.Interface, ns, selector, test.Logger)
		_ = kubernetes.DeleteDeploymentsWithLabels(ctx, test.K8sClient.Interface, ns, selector, test.Logger)
	}
}

func labelsToSelector(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// getRouteTableIDs finds the route tables associated with the cluster subnets.
// These are the same route tables where the hybrid nodes VPC CIDR route exists.
func getRouteTableIDs(ctx context.Context, test *suite.PeeredVPCTest) string {
	output, err := test.EC2Client.DescribeRouteTables(ctx, &ec2v2.DescribeRouteTablesInput{
		Filters: []ec2v2types.Filter{
			{
				Name:   aws.String("association.subnet-id"),
				Values: test.Cluster.SubnetIds,
			},
		},
	})
	Expect(err).NotTo(HaveOccurred(), "should describe route tables for cluster subnets")

	seen := make(map[string]bool)
	var ids []string
	for _, rt := range output.RouteTables {
		id := aws.ToString(rt.RouteTableId)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	return strings.Join(ids, ",")
}

// attachGatewayPolicy attaches an inline IAM policy to the MNG node role
// granting the EC2 permissions the gateway needs to manage route tables.
func attachGatewayPolicy(ctx context.Context, test *suite.PeeredVPCTest) {
	roleName := roleNameFromARN(test.StackOut.ManagedNodeRoleArn)

	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Action": []string{
					"ec2:DescribeInstances",
					"ec2:DescribeRouteTables",
					"ec2:CreateRoute",
					"ec2:ReplaceRoute",
				},
				"Resource": "*",
			},
		},
	}

	policyJSON, err := json.Marshal(policy)
	Expect(err).NotTo(HaveOccurred())

	_, err = test.IAMClient.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(gatewayPolicyName),
		PolicyDocument: aws.String(string(policyJSON)),
	})
	Expect(err).NotTo(HaveOccurred(), "should attach gateway policy to MNG role %s", roleName)
	test.Logger.Info("Attached gateway IAM policy", "role", roleName, "policy", gatewayPolicyName)

	DeferCleanup(func(ctx context.Context) {
		if test.SkipCleanup {
			return
		}
		test.Logger.Info("Removing gateway IAM policy", "role", roleName)
		_, _ = test.IAMClient.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   aws.String(roleName),
			PolicyName: aws.String(gatewayPolicyName),
		})
	})
}

// getClusterSecurityGroup returns the EKS-managed cluster security group ID.
// This is different from test.Cluster.SecurityGroupID which is the hybrid VPC default SG.
func getClusterSecurityGroup(ctx context.Context, test *suite.PeeredVPCTest) string {
	output, err := test.EKSClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(test.Cluster.Name),
	})
	Expect(err).NotTo(HaveOccurred(), "should describe cluster")
	sgID := aws.ToString(output.Cluster.ResourcesVpcConfig.ClusterSecurityGroupId)
	Expect(sgID).NotTo(BeEmpty(), "cluster security group should be set")
	return sgID
}

// allowVXLANIngress adds an ingress rule for VXLAN (UDP 8472) on the given security group.
// The rule is not cleaned up because the security group is deleted with the cluster.
func allowVXLANIngress(ctx context.Context, test *suite.PeeredVPCTest, sgID string) {
	_, err := test.EC2Client.AuthorizeSecurityGroupIngress(ctx, &ec2v2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []ec2v2types.IpPermission{
			{
				IpProtocol: aws.String("udp"),
				FromPort:   aws.Int32(vxlanPort),
				ToPort:     aws.Int32(vxlanPort),
				IpRanges: []ec2v2types.IpRange{
					// TODO (pokearu): Scope to exact CIDR
					{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("VXLAN from hybrid nodes")},
				},
			},
		},
	})
	Expect(err).NotTo(HaveOccurred(), "should allow VXLAN ingress on security group %s", sgID)
	test.Logger.Info("Added VXLAN ingress rule", "sgID", sgID, "port", vxlanPort)
}

// roleNameFromARN extracts the role name from an IAM role ARN.
// Handles both simple roles and path-based roles.
// arn:aws:iam::123456789012:role/MyRole -> MyRole
// arn:aws:iam::123456789012:role/path/MyRole -> MyRole
func roleNameFromARN(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// upgradeCilium replaces the framework-installed Cilium with a custom chart.
// It applies a pre-rendered Cilium 1.19.0 template directly via the k8s API,
// bypassing Helm which has compatibility issues with framework-installed resources.
func upgradeCilium(ctx context.Context, test *suite.PeeredVPCTest) {
	tmpl, err := template.New("cilium").Parse(string(ciliumTemplate))
	Expect(err).NotTo(HaveOccurred(), "should parse Cilium template")

	var rendered bytes.Buffer
	err = tmpl.Execute(&rendered, map[string]string{
		"PodCIDR": "10.87.0.0/16",
	})
	Expect(err).NotTo(HaveOccurred(), "should render Cilium template")

	objs, err := kubernetes.YamlToUnstructured(rendered.Bytes())
	Expect(err).NotTo(HaveOccurred(), "should parse rendered Cilium YAML")

	test.Logger.Info("Applying Cilium 1.19.0 template", "resources", len(objs))
	err = kubernetes.UpsertManifestsWithRetries(ctx, test.K8sClient.Dynamic, objs)
	Expect(err).NotTo(HaveOccurred(), "should apply Cilium template")

	test.Logger.Info("Waiting for cilium DaemonSet to be ready")
	Eventually(func() bool {
		ds, err := test.K8sClient.Interface.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return ds.Status.DesiredNumberScheduled > 0 &&
			ds.Status.DesiredNumberScheduled == ds.Status.NumberReady &&
			ds.Status.UpdatedNumberScheduled == ds.Status.DesiredNumberScheduled
	}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "cilium DaemonSet should become ready")

	test.Logger.Info("Cilium upgraded successfully")
}
