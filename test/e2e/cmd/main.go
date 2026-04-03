package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/cleanup"
	"github.com/aws/eks-hybrid/test/e2e/cluster"
	"github.com/aws/eks-hybrid/test/e2e/run"
	"github.com/go-logr/logr"
)

const (
	nodeadmAMDURL     = "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/amd64/nodeadm"
	nodeadmARMURL     = "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/arm64/nodeadm"
	clusterNamePrefix = "gateway-e2e"
	cni               = "cilium"
	defaultTimeout    = 60 * time.Minute
	sweepAgeThreshold = 6 * time.Hour
)

var (
	awsRegion         = envOrDefault("AWS_REGION", "us-west-2")
	kubernetesVersion = envOrDefault("K8S_VERSION", "1.31")
	skipCleanup       = os.Getenv("SKIP_CLEANUP") == "true"
	testBinaryPath    = envOrDefault("TEST_BINARY", "bin/gateway.test")
	artifactsDir      = envOrDefault("ARTIFACTS_DIR", "/tmp/gateway-e2e")
)

type GatewayE2ETest struct {
	awsCfg      aws.Config
	clusterName string
	logger      logr.Logger
	timeout     time.Duration
}

func main() {
	if err := run_(); err != nil {
		log.Fatal(err)
	}
}

func run_() error {
	ctx := context.Background()

	for _, required := range []string{"GATEWAY_IMAGE", "GATEWAY_CHART", "GATEWAY_CHART_VERSION"} {
		if os.Getenv(required) == "" {
			return fmt.Errorf("%s must be set", required)
		}
	}

	timeout, err := time.ParseDuration(envOrDefault("E2E_TIMEOUT", "60m"))
	if err != nil {
		timeout = defaultTimeout
	}

	logger := e2e.NewLogger()

	awsCfg, err := e2e.NewAWSConfig(ctx,
		awsconfig.WithRegion(awsRegion),
		awsconfig.WithAppID("gateway-e2e"),
		awsconfig.WithRetryer(func() aws.Retryer {
			return retry.AddWithMaxBackoffDelay(
				retry.AddWithMaxAttempts(retry.NewStandard(), 10),
				10*time.Second,
			)
		}),
	)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	clusterName := fmt.Sprintf("%s-%s-%d", clusterNamePrefix, replaceDotsWithDashes(kubernetesVersion), time.Now().Unix())

	runner := GatewayE2ETest{
		awsCfg:      awsCfg,
		clusterName: clusterName,
		logger:      logger,
		timeout:     timeout,
	}

	return runner.Run(ctx)
}

func (g *GatewayE2ETest) Run(ctx context.Context) error {
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return fmt.Errorf("creating artifacts directory: %w", err)
	}

	// Sweep old clusters from previous runs
	g.logger.Info("Sweeping old test resources", "prefix", clusterNamePrefix)
	sweeper := cleanup.NewSweeper(g.awsCfg, g.logger, "")
	if err := sweeper.Run(ctx, cleanup.SweeperInput{
		ClusterNamePrefix:    clusterNamePrefix + "-",
		DryRun:               skipCleanup,
		InstanceAgeThreshold: sweepAgeThreshold,
	}); err != nil {
		g.logger.Error(err, "Sweeper failed, continuing with tests")
	}

	g.logger.Info("Using test binary", "path", testBinaryPath)

	ginkgoPath, err := exec.LookPath("ginkgo")
	if err != nil {
		return fmt.Errorf("ginkgo not found on PATH (run 'make ginkgo' to install): %w", err)
	}

	e2eRunner := run.E2E{
		AwsCfg: g.awsCfg,
		Logger: g.logger,
		Paths: run.E2EPaths{
			Ginkgo:              ginkgoPath,
			TestsBinaryOrSource: testBinaryPath,
		},
		TestConfig:    g.getTestConfig(),
		TestResources: g.getTestResources(),
		TestProcs:     1,
		Timeout:       g.timeout,
		SkipCleanup:   skipCleanup,
	}

	g.logger.Info("Starting gateway e2e tests",
		"cluster", g.clusterName,
		"region", awsRegion,
		"k8sVersion", kubernetesVersion,
		"skipCleanup", skipCleanup,
	)

	result, testErr := e2eRunner.Run(ctx)

	if outputErr := e2eRunner.PrintResults(ctx, result); outputErr != nil {
		g.logger.Error(outputErr, "Failed to print results")
	}

	if testErr != nil {
		return fmt.Errorf("e2e tests failed: %w", testErr)
	}

	// Verify the test execution phase actually ran
	var executePhase run.Phase
	for _, phase := range result.Phases {
		if phase.Name == "ExecuteTests" {
			executePhase = phase
			break
		}
	}

	if executePhase.Name == "" {
		return errors.New("ExecuteTests phase not found")
	}

	if executePhase.Status != "success" {
		return fmt.Errorf("ExecuteTests phase failed: %w", executePhase.Error)
	}

	g.logger.Info("All e2e tests passed", "ran", result.TestRan, "failed", result.TestFailed)
	return nil
}

func (g *GatewayE2ETest) getTestConfig() e2e.TestConfig {
	return e2e.TestConfig{
		ClusterName:     g.clusterName,
		ClusterRegion:   awsRegion,
		NodeadmUrlAMD:   nodeadmAMDURL,
		NodeadmUrlARM:   nodeadmARMURL,
		ArtifactsFolder: artifactsDir,
	}
}

func (g *GatewayE2ETest) getTestResources() cluster.TestResources {
	return cluster.SetTestResourcesDefaults(cluster.TestResources{
		ClusterName:       g.clusterName,
		ClusterRegion:     awsRegion,
		KubernetesVersion: kubernetesVersion,
		Cni:               cni,
	})
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func replaceDotsWithDashes(s string) string {
	return strings.ReplaceAll(s, ".", "-")
}
