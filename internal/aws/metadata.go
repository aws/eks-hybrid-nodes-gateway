package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ec2MetadataClient is a simple client for EC2 instance metadata service (IMDSv2)
type ec2MetadataClient struct {
	baseURL string
	client  *http.Client
}

// getToken retrieves an IMDSv2 session token
func (c *ec2MetadataClient) getToken(ctx context.Context) (string, error) {
	if c.client == nil {
		c.client = &http.Client{
			Timeout: 2 * time.Second,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/latest/api/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get IMDSv2 token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get IMDSv2 token: status %d", resp.StatusCode)
	}

	token, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(token), nil
}

// getMetadata retrieves metadata from the given path
func (c *ec2MetadataClient) getMetadata(ctx context.Context, path string) (string, error) {
	if c.client == nil {
		c.client = &http.Client{
			Timeout: 2 * time.Second,
		}
	}

	// Get IMDSv2 token
	token, err := c.getToken(ctx)
	if err != nil {
		return "", err
	}

	// Request metadata with token
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get metadata from %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get metadata from %s: status %d", path, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// getInstanceID retrieves the instance ID from metadata
func (c *ec2MetadataClient) getInstanceID(ctx context.Context) (string, error) {
	return c.getMetadata(ctx, "/latest/meta-data/instance-id")
}

// getRegion retrieves the region from metadata
func (c *ec2MetadataClient) getRegion(ctx context.Context) (string, error) {
	// Get availability zone first
	az, err := c.getMetadata(ctx, "/latest/meta-data/placement/availability-zone")
	if err != nil {
		return "", err
	}

	// Region is the AZ without the last character (e.g., us-west-2a -> us-west-2)
	if len(az) < 2 {
		return "", fmt.Errorf("invalid availability zone: %s", az)
	}

	return az[:len(az)-1], nil
}
