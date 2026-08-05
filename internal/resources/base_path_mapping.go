// internal/resources/base_path_mapping.go
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigatewayTypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// BasePathMappingManager handles AWS API Gateway base path mapping operations
// for custom domain integration.
type BasePathMappingManager struct {
	client *apigateway.Client
	config *DispatcherConfig
}

// NewBasePathMappingManager creates a new BasePathMappingManager instance.
func NewBasePathMappingManager(client *apigateway.Client, config *DispatcherConfig) *BasePathMappingManager {
	return &BasePathMappingManager{
		client: client,
		config: config,
	}
}

// ValidateCustomDomainExists checks if the custom domain exists in AWS API Gateway.
// Returns an error with clear remediation steps if the domain is not found.
func (m *BasePathMappingManager) ValidateCustomDomainExists(ctx context.Context, domainName string) error {
	utils.Info(ctx, "Validating custom domain exists", map[string]interface{}{
		"domain_name": domainName,
	})

	input := &apigateway.GetDomainNameInput{
		DomainName: aws.String(domainName),
	}

	_, err := m.client.GetDomainName(ctx, input)
	if err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "NotFoundException") || strings.Contains(err.Error(), "NotFound") {
			return fmt.Errorf("custom domain '%s' does not exist in AWS API Gateway. "+
				"Create it first using aws_api_gateway_domain_name resource in your Terraform configuration. "+
				"See the design document for example Terraform code", domainName)
		}
		// Other errors (permissions, network, etc.)
		return fmt.Errorf("failed to validate custom domain '%s': %w", domainName, err)
	}

	utils.Info(ctx, "Custom domain validated successfully", map[string]interface{}{
		"domain_name": domainName,
	})

	return nil
}

// CreateBasePathMapping creates a base path mapping from a path to an API Gateway.
// The basePath should be the gateway name (e.g., "ops" for /ops path).
// This function includes retry logic for transient "Invalid stage identifier" errors
// that can occur due to AWS API Gateway eventual consistency.
func (m *BasePathMappingManager) CreateBasePathMapping(
	ctx context.Context,
	domainName string,
	basePath string,
	restApiId string,
	stageName string,
) error {
	utils.Info(ctx, "Creating base path mapping", map[string]interface{}{
		"domain_name": domainName,
		"base_path":   basePath,
		"rest_api_id": restApiId,
		"stage_name":  stageName,
	})

	input := &apigateway.CreateBasePathMappingInput{
		DomainName: aws.String(domainName),
		BasePath:   aws.String(basePath),
		RestApiId:  aws.String(restApiId),
		Stage:      aws.String(stageName),
	}

	// Retry logic for transient "Invalid stage identifier" errors
	// AWS API Gateway has eventual consistency - stage may not be immediately available
	maxRetries := 5
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		_, err := m.client.CreateBasePathMapping(ctx, input)
		if err == nil {
			utils.Info(ctx, "Successfully created base path mapping", map[string]interface{}{
				"domain_name": domainName,
				"base_path":   basePath,
				"rest_api_id": restApiId,
				"attempt":     attempt,
			})
			return nil
		}

		lastErr = err

		// Check for conflict error (path already mapped) - don't retry
		if strings.Contains(err.Error(), "ConflictException") || strings.Contains(err.Error(), "Conflict") {
			// Try to get the existing mapping to provide better error message
			existingMapping, getErr := m.getBasePathMapping(ctx, domainName, basePath)
			if getErr == nil && existingMapping != nil {
				return fmt.Errorf("base path '/%s' is already mapped to API Gateway '%s' on domain '%s'. "+
					"Remove the existing mapping or use a different gateway name",
					basePath, aws.ToString(existingMapping.RestApiId), domainName)
			}
			return fmt.Errorf("base path '/%s' is already mapped by another API Gateway on domain '%s'. "+
				"Remove the existing mapping or use a different gateway name", basePath, domainName)
		}

		// Check for "Invalid stage identifier" - this is retryable (eventual consistency)
		if strings.Contains(err.Error(), "Invalid stage identifier") || strings.Contains(err.Error(), "BadRequestException") {
			if attempt < maxRetries {
				waitTime := time.Duration(attempt*2) * time.Second
				utils.Info(ctx, "Stage not yet available, retrying", map[string]interface{}{
					"domain_name": domainName,
					"base_path":   basePath,
					"rest_api_id": restApiId,
					"stage_name":  stageName,
					"attempt":     attempt,
					"wait_time":   waitTime.String(),
					"error":       err.Error(),
				})
				time.Sleep(waitTime)
				continue
			}
		}

		// Check for stage not found error (NotFoundException) - don't retry
		if strings.Contains(err.Error(), "NotFoundException") && strings.Contains(err.Error(), "stage") {
			return fmt.Errorf("stage '%s' does not exist for API Gateway '%s'. "+
				"Ensure the deployment completed successfully before creating base path mappings",
				stageName, restApiId)
		}

		// For other errors, don't retry
		return fmt.Errorf("failed to create base path mapping for '/%s' on domain '%s': %w",
			basePath, domainName, err)
	}

	// All retries exhausted
	return fmt.Errorf("failed to create base path mapping for '/%s' on domain '%s' after %d attempts: %w",
		basePath, domainName, maxRetries, lastErr)
}

// UpdateBasePathMapping updates an existing base path mapping to point to a new API Gateway.
// This is used when an API Gateway is recreated with a new ID.
func (m *BasePathMappingManager) UpdateBasePathMapping(
	ctx context.Context,
	domainName string,
	basePath string,
	restApiId string,
	stageName string,
) error {
	utils.Info(ctx, "Updating base path mapping", map[string]interface{}{
		"domain_name": domainName,
		"base_path":   basePath,
		"rest_api_id": restApiId,
		"stage_name":  stageName,
	})

	// AWS API Gateway UpdateBasePathMapping uses PATCH operations
	patchOps := []apigatewayTypes.PatchOperation{
		{
			Op:    apigatewayTypes.OpReplace,
			Path:  aws.String("/restapiId"),
			Value: aws.String(restApiId),
		},
		{
			Op:    apigatewayTypes.OpReplace,
			Path:  aws.String("/stage"),
			Value: aws.String(stageName),
		},
	}

	input := &apigateway.UpdateBasePathMappingInput{
		DomainName:      aws.String(domainName),
		BasePath:        aws.String(basePath),
		PatchOperations: patchOps,
	}

	_, err := m.client.UpdateBasePathMapping(ctx, input)
	if err != nil {
		// Check for not found error
		if strings.Contains(err.Error(), "NotFoundException") || strings.Contains(err.Error(), "NotFound") {
			// Mapping doesn't exist, try to create it instead
			utils.Info(ctx, "Base path mapping not found, creating new mapping", map[string]interface{}{
				"domain_name": domainName,
				"base_path":   basePath,
			})
			return m.CreateBasePathMapping(ctx, domainName, basePath, restApiId, stageName)
		}

		// Check for stage not found error
		if strings.Contains(err.Error(), "stage") {
			return fmt.Errorf("stage '%s' does not exist for API Gateway '%s'. "+
				"Ensure the deployment completed successfully before updating base path mappings. "+
				"AWS error: %w", stageName, restApiId, err)
		}

		return fmt.Errorf("failed to update base path mapping for '/%s' on domain '%s': %w",
			basePath, domainName, err)
	}

	utils.Info(ctx, "Successfully updated base path mapping", map[string]interface{}{
		"domain_name": domainName,
		"base_path":   basePath,
		"rest_api_id": restApiId,
	})

	return nil
}

// DeleteBasePathMapping removes a base path mapping.
// This operation is idempotent - it succeeds even if the mapping doesn't exist.
func (m *BasePathMappingManager) DeleteBasePathMapping(
	ctx context.Context,
	domainName string,
	basePath string,
) error {
	utils.Info(ctx, "Deleting base path mapping", map[string]interface{}{
		"domain_name": domainName,
		"base_path":   basePath,
	})

	input := &apigateway.DeleteBasePathMappingInput{
		DomainName: aws.String(domainName),
		BasePath:   aws.String(basePath),
	}

	_, err := m.client.DeleteBasePathMapping(ctx, input)
	if err != nil {
		// Handle not found gracefully (idempotent delete)
		if strings.Contains(err.Error(), "NotFoundException") || strings.Contains(err.Error(), "NotFound") {
			utils.Info(ctx, "Base path mapping already deleted or doesn't exist", map[string]interface{}{
				"domain_name": domainName,
				"base_path":   basePath,
			})
			return nil
		}

		return fmt.Errorf("failed to delete base path mapping for '/%s' on domain '%s': %w",
			basePath, domainName, err)
	}

	utils.Info(ctx, "Successfully deleted base path mapping", map[string]interface{}{
		"domain_name": domainName,
		"base_path":   basePath,
	})

	return nil
}

// GetBasePathMappings retrieves all base path mappings for a custom domain.
// Returns a map of base path to API Gateway ID.
func (m *BasePathMappingManager) GetBasePathMappings(
	ctx context.Context,
	domainName string,
) (map[string]string, error) {
	utils.Info(ctx, "Getting base path mappings", map[string]interface{}{
		"domain_name": domainName,
	})

	mappings := make(map[string]string)
	var position *string

	for {
		input := &apigateway.GetBasePathMappingsInput{
			DomainName: aws.String(domainName),
			Limit:      aws.Int32(500),
			Position:   position,
		}

		output, err := m.client.GetBasePathMappings(ctx, input)
		if err != nil {
			// Handle domain not found
			if strings.Contains(err.Error(), "NotFoundException") || strings.Contains(err.Error(), "NotFound") {
				utils.Warn(ctx, "Custom domain not found when getting base path mappings", map[string]interface{}{
					"domain_name": domainName,
				})
				return mappings, nil
			}

			return nil, fmt.Errorf("failed to get base path mappings for domain '%s': %w", domainName, err)
		}

		for _, mapping := range output.Items {
			basePath := aws.ToString(mapping.BasePath)
			restApiId := aws.ToString(mapping.RestApiId)

			// Handle empty base path (root mapping)
			if basePath == "(none)" || basePath == "" {
				basePath = ""
			}

			mappings[basePath] = restApiId
		}

		// Check for more pages
		if output.Position == nil || *output.Position == "" {
			break
		}
		position = output.Position
	}

	utils.Info(ctx, "Retrieved base path mappings", map[string]interface{}{
		"domain_name": domainName,
		"count":       len(mappings),
	})

	return mappings, nil
}

// getBasePathMapping retrieves a single base path mapping (internal helper).
func (m *BasePathMappingManager) getBasePathMapping(
	ctx context.Context,
	domainName string,
	basePath string,
) (*apigateway.GetBasePathMappingOutput, error) {
	input := &apigateway.GetBasePathMappingInput{
		DomainName: aws.String(domainName),
		BasePath:   aws.String(basePath),
	}

	return m.client.GetBasePathMapping(ctx, input)
}

// VerifyStageExists checks if a stage exists for an API Gateway and waits for it to become available.
// This handles AWS eventual consistency where a stage may not be immediately available after creation.
func (m *BasePathMappingManager) VerifyStageExists(ctx context.Context, restApiId string, stageName string) error {
	maxRetries := 15
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		input := &apigateway.GetStageInput{
			RestApiId: aws.String(restApiId),
			StageName: aws.String(stageName),
		}

		_, err := m.client.GetStage(ctx, input)
		if err == nil {
			utils.Info(ctx, "Stage verified", map[string]interface{}{
				"rest_api_id": restApiId,
				"stage_name":  stageName,
				"attempt":     attempt,
			})
			return nil
		}

		lastErr = err

		// Check if it's a not found error - this is retryable (eventual consistency)
		if strings.Contains(err.Error(), "NotFoundException") || strings.Contains(err.Error(), "NotFound") {
			if attempt < maxRetries {
				// Exponential backoff: 2s, 4s, 6s... up to 30s
				waitTime := time.Duration(attempt*2) * time.Second
				if waitTime > 30*time.Second {
					waitTime = 30 * time.Second
				}
				utils.Info(ctx, "Stage not yet available, waiting", map[string]interface{}{
					"rest_api_id": restApiId,
					"stage_name":  stageName,
					"attempt":     attempt,
					"wait_time":   waitTime.String(),
				})
				time.Sleep(waitTime)
				continue
			}
		}

		// For other errors, fail immediately
		return fmt.Errorf("failed to verify stage '%s' for API Gateway '%s': %w", stageName, restApiId, err)
	}

	return fmt.Errorf("stage '%s' not found for API Gateway '%s' after %d attempts: %w",
		stageName, restApiId, maxRetries, lastErr)
}

// SyncBasePathMappings synchronizes base path mappings with the current set of gateways.
// It creates mappings for new gateways, updates mappings for changed API Gateway IDs,
// and deletes mappings for removed gateways.
func (m *BasePathMappingManager) SyncBasePathMappings(
	ctx context.Context,
	domainName string,
	gatewayIds map[string]string, // gateway name -> API Gateway ID
	stageName string,
) (map[string]string, error) {
	utils.Info(ctx, "Syncing base path mappings", map[string]interface{}{
		"domain_name":   domainName,
		"gateway_count": len(gatewayIds),
	})

	// First, verify all stages exist before attempting any mappings
	// This handles AWS eventual consistency issues
	var stageErrors []string
	for gateway, apiId := range gatewayIds {
		if err := m.VerifyStageExists(ctx, apiId, stageName); err != nil {
			stageErrors = append(stageErrors, fmt.Sprintf("%s: %v", gateway, err))
		}
	}

	if len(stageErrors) > 0 {
		return nil, fmt.Errorf("some stages are not available: %s", strings.Join(stageErrors, "; "))
	}

	// Get current mappings
	currentMappings, err := m.GetBasePathMappings(ctx, domainName)
	if err != nil {
		return nil, fmt.Errorf("failed to get current base path mappings: %w", err)
	}

	// Track results
	resultMappings := make(map[string]string)
	var errors []string

	// Create or update mappings for each gateway
	for gateway, apiId := range gatewayIds {
		basePath := gateway // Use gateway name as base path

		if existingApiId, exists := currentMappings[basePath]; exists {
			// Mapping exists - check if it needs update
			if existingApiId != apiId {
				// API Gateway ID changed, update the mapping
				err := m.UpdateBasePathMapping(ctx, domainName, basePath, apiId, stageName)
				if err != nil {
					errors = append(errors, fmt.Sprintf("failed to update mapping for '%s': %v", gateway, err))
					continue
				}
			}
			// Mapping is correct
			resultMappings[basePath] = apiId
		} else {
			// New gateway, create mapping
			err := m.CreateBasePathMapping(ctx, domainName, basePath, apiId, stageName)
			if err != nil {
				errors = append(errors, fmt.Sprintf("failed to create mapping for '%s': %v", gateway, err))
				continue
			}
			resultMappings[basePath] = apiId
		}
	}

	// Delete mappings for removed gateways
	for basePath := range currentMappings {
		if _, exists := gatewayIds[basePath]; !exists {
			// Gateway was removed, delete the mapping
			err := m.DeleteBasePathMapping(ctx, domainName, basePath)
			if err != nil {
				errors = append(errors, fmt.Sprintf("failed to delete mapping for '%s': %v", basePath, err))
			}
		}
	}

	if len(errors) > 0 {
		return resultMappings, fmt.Errorf("some base path mapping operations failed: %s", strings.Join(errors, "; "))
	}

	utils.Info(ctx, "Successfully synced base path mappings", map[string]interface{}{
		"domain_name":    domainName,
		"mapping_count":  len(resultMappings),
	})

	return resultMappings, nil
}

// BuildGatewayURLs constructs the full URLs for each gateway using the custom domain.
// Returns a map of gateway name to full URL (e.g., {"ops": "https://api.example.com/ops"}).
func (m *BasePathMappingManager) BuildGatewayURLs(
	domainName string,
	gateways []string,
) map[string]string {
	urls := make(map[string]string)

	for _, gateway := range gateways {
		urls[gateway] = fmt.Sprintf("https://%s/%s", domainName, gateway)
	}

	return urls
}

// BuildCustomDomainURL constructs the base URL for the custom domain.
func (m *BasePathMappingManager) BuildCustomDomainURL(domainName string) string {
	return fmt.Sprintf("https://%s", domainName)
}
