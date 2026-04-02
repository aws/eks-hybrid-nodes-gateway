package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
)

// RouteTableManager manages AWS route table entries for hybrid pod CIDRs
type RouteTableManager struct {
	ec2Client     *ec2.Client
	routeTableIDs []string
	instanceID    string
	primaryENI    string
	logger        logr.Logger
	region        string
}

// NewRouteTableManager creates a new route table manager
func NewRouteTableManager(ctx context.Context, routeTableIDs []string, instanceID, region string, logger logr.Logger) (*RouteTableManager, error) {
	if len(routeTableIDs) == 0 {
		return nil, fmt.Errorf("no route table IDs provided")
	}

	if instanceID == "" {
		return nil, fmt.Errorf("instance ID is required")
	}

	// Load AWS SDK configuration
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(cfg)

	// Get the primary ENI for this instance
	primaryENI, err := getPrimaryENI(ctx, ec2Client, instanceID, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get primary ENI for instance %s: %w", instanceID, err)
	}

	logger.Info("Route table manager ready", "primaryENI", primaryENI)

	return &RouteTableManager{
		ec2Client:     ec2Client,
		routeTableIDs: routeTableIDs,
		instanceID:    instanceID,
		primaryENI:    primaryENI,
		logger:        logger,
		region:        region,
	}, nil
}

// getPrimaryENI retrieves the primary network interface ID for an instance
func getPrimaryENI(ctx context.Context, client *ec2.Client, instanceID string, logger logr.Logger) (string, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "", fmt.Errorf("instance %s not found", instanceID)
	}

	instance := result.Reservations[0].Instances[0]

	// Find the primary network interface (DeviceIndex = 0)
	for _, eni := range instance.NetworkInterfaces {
		if eni.Attachment != nil && aws.ToInt32(eni.Attachment.DeviceIndex) == 0 {
			return aws.ToString(eni.NetworkInterfaceId), nil
		}
	}

	return "", fmt.Errorf("no primary network interface found for instance %s", instanceID)
}

// UpdateRoutes updates route table entries to point pod CIDRs to this instance's primary ENI
func (m *RouteTableManager) UpdateRoutes(ctx context.Context, podCIDRs []string) error {
	m.logger.Info("Updating route tables", "podCIDRs", podCIDRs, "primaryENI", m.primaryENI)

	for _, routeTableID := range m.routeTableIDs {
		for _, cidr := range podCIDRs {
			if err := m.updateRoute(ctx, routeTableID, cidr); err != nil {
				return fmt.Errorf("failed to update route for CIDR %s in table %s: %w", cidr, routeTableID, err)
			}
		}
	}

	return nil
}

// updateRoute updates or creates a single route entry
func (m *RouteTableManager) updateRoute(ctx context.Context, routeTableID, cidr string) error {
	// First, check if the route already exists
	existingRoute, err := m.getRoute(ctx, routeTableID, cidr)
	if err != nil {
		return err
	}

	if existingRoute != nil {
		// Route exists, check if it needs updating
		if existingRoute.NetworkInterfaceId != nil && *existingRoute.NetworkInterfaceId == m.primaryENI {
			return nil // already correct
		}

		// Route exists but points to different target, replace it
		m.logger.Info("Replacing existing route",
			"routeTable", routeTableID,
			"cidr", cidr,
			"oldENI", aws.ToString(existingRoute.NetworkInterfaceId),
			"oldInstance", aws.ToString(existingRoute.InstanceId),
			"newENI", m.primaryENI,
		)
		return m.replaceRoute(ctx, routeTableID, cidr)
	}

	return m.createRoute(ctx, routeTableID, cidr)
}

// getRoute retrieves an existing route from the route table
func (m *RouteTableManager) getRoute(ctx context.Context, routeTableID, cidr string) (*types.Route, error) {
	input := &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	}

	result, err := m.ec2Client.DescribeRouteTables(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe route table: %w", err)
	}

	if len(result.RouteTables) == 0 {
		return nil, fmt.Errorf("route table %s not found", routeTableID)
	}

	// Find the route with matching destination CIDR
	for _, route := range result.RouteTables[0].Routes {
		if route.DestinationCidrBlock != nil && *route.DestinationCidrBlock == cidr {
			return &route, nil
		}
	}

	return nil, nil // Route not found
}

// createRoute creates a new route in the route table using ENI
func (m *RouteTableManager) createRoute(ctx context.Context, routeTableID, cidr string) error {
	input := &ec2.CreateRouteInput{
		RouteTableId:         aws.String(routeTableID),
		DestinationCidrBlock: aws.String(cidr),
		NetworkInterfaceId:   aws.String(m.primaryENI),
	}

	_, err := m.ec2Client.CreateRoute(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to create route: %w", err)
	}

	return nil
}

// replaceRoute replaces an existing route in the route table using ENI
func (m *RouteTableManager) replaceRoute(ctx context.Context, routeTableID, cidr string) error {
	input := &ec2.ReplaceRouteInput{
		RouteTableId:         aws.String(routeTableID),
		DestinationCidrBlock: aws.String(cidr),
		NetworkInterfaceId:   aws.String(m.primaryENI),
	}

	_, err := m.ec2Client.ReplaceRoute(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to replace route: %w", err)
	}

	return nil
}

// GetCurrentInstanceID retrieves the instance ID from EC2 metadata
func GetCurrentInstanceID(ctx context.Context) (string, error) {
	// Try IMDSv2 first
	client := &ec2MetadataClient{
		baseURL: "http://169.254.169.254",
	}

	instanceID, err := client.getInstanceID(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get instance ID from metadata: %w", err)
	}

	return instanceID, nil
}

// GetCurrentRegion retrieves the region from EC2 metadata
func GetCurrentRegion(ctx context.Context) (string, error) {
	client := &ec2MetadataClient{
		baseURL: "http://169.254.169.254",
	}

	region, err := client.getRegion(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get region from metadata: %w", err)
	}

	return region, nil
}

// VerifyRouteTableAccess verifies that the instance has permission to modify route tables
func (m *RouteTableManager) VerifyRouteTableAccess(ctx context.Context) error {
	for _, routeTableID := range m.routeTableIDs {
		input := &ec2.DescribeRouteTablesInput{
			RouteTableIds: []string{routeTableID},
		}

		verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, err := m.ec2Client.DescribeRouteTables(verifyCtx, input)
		cancel()

		if err != nil {
			return fmt.Errorf("failed to access route table %s: %w (ensure IAM permissions are correct)", routeTableID, err)
		}

		if len(result.RouteTables) == 0 {
			return fmt.Errorf("route table %s not found", routeTableID)
		}
	}

	return nil
}

// ParseRouteTableIDs parses a comma-separated list of route table IDs
func ParseRouteTableIDs(routeTableIDsStr string) []string {
	if routeTableIDsStr == "" {
		return nil
	}

	ids := strings.Split(routeTableIDsStr, ",")
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			result = append(result, id)
		}
	}
	return result
}
