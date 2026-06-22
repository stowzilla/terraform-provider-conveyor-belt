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

// createGatewayViaOpenAPI creates a new API Gateway using PutRestApi with an OpenAPI spec.
func (pm *ParallelManager) createGatewayViaOpenAPI(
	ctx context.Context,
	gatewayName string,
	apiName string,
	existingAPIID string,
	gatewayRoutes []utils.Route,
	allRoutes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
	models []utils.ModelDefinition,
) GatewayResult {
	result := GatewayResult{Gateway: gatewayName}

	// Generate OpenAPI spec
	gen := NewOpenAPIGenerator(config)
	specJSON, err := gen.GenerateSpecJSON(ctx, gatewayName, allRoutes, lambdaARNs, models)
	if err != nil {
		result.Error = fmt.Errorf("failed to generate OpenAPI spec for gateway '%s': %w", gatewayName, err)
		return result
	}

	utils.Info(ctx, "Generated OpenAPI spec for PutRestApi", map[string]interface{}{
		"gateway":      gatewayName,
		"spec_size":    len(specJSON),
		"models_count": len(models),
	})

	apiID := existingAPIID

	if apiID == "" {
		// Create a new REST API first, then overwrite with spec
		var createErr error
		for attempt := 0; attempt < 10; attempt++ {
			createOutput, err := pm.clients.ApiGateway.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
				Name:        aws.String(apiName),
				Description: aws.String(fmt.Sprintf("API Gateway for %s in %s-%s", gatewayName, config.AppName, config.Environment)),
				Tags:        buildResourceTags(config, "ApiGateway", apiName),
			})
			if err == nil {
				apiID = *createOutput.Id
				break
			}
			createErr = err
			if strings.Contains(err.Error(), "TooManyRequests") || strings.Contains(err.Error(), "429") {
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
				continue
			}
			result.Error = fmt.Errorf("failed to create REST API: %w", err)
			return result
		}
		if apiID == "" {
			result.Error = fmt.Errorf("failed to create REST API after retries: %w", createErr)
			return result
		}
	}

	// Use PutRestApi to import the OpenAPI spec
	if err := pm.putRestApiWithRetry(ctx, apiID, specJSON, gatewayName); err != nil {
		result.Error = err
		return result
	}

	// Create deployment
	invokeURL, err := pm.deployGateway(ctx, apiID, gatewayName, config)
	if err != nil {
		result.Error = err
		return result
	}

	// Add Lambda permissions
	if err := pm.addLambdaPermissionsForGateway(ctx, apiID, gatewayRoutes, lambdaARNs, config); err != nil {
		utils.Warn(ctx, "Failed to add some Lambda permissions", map[string]interface{}{
			"gateway": gatewayName,
			"error":   err.Error(),
		})
	}

	result.ID = apiID
	result.URL = invokeURL
	result.Success = true

	utils.Info(ctx, "Successfully created API Gateway via OpenAPI import", map[string]interface{}{
		"gateway": gatewayName,
		"api_id":  apiID,
	})

	return result
}

// updateGatewayViaOpenAPI updates an existing API Gateway using PutRestApi with an OpenAPI spec.
func (pm *ParallelManager) updateGatewayViaOpenAPI(
	ctx context.Context,
	gatewayName string,
	apiID string,
	gatewayRoutes []utils.Route,
	allRoutes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
	models []utils.ModelDefinition,
) GatewayResult {
	result := GatewayResult{Gateway: gatewayName}

	// Generate OpenAPI spec
	gen := NewOpenAPIGenerator(config)
	specJSON, err := gen.GenerateSpecJSON(ctx, gatewayName, allRoutes, lambdaARNs, models)
	if err != nil {
		result.Error = fmt.Errorf("failed to generate OpenAPI spec for gateway '%s': %w", gatewayName, err)
		return result
	}

	utils.Info(ctx, "Generated OpenAPI spec for PutRestApi update", map[string]interface{}{
		"gateway":   gatewayName,
		"api_id":    apiID,
		"spec_size": len(specJSON),
	})

	// Use PutRestApi to overwrite the API with the new spec
	if err := pm.putRestApiWithRetry(ctx, apiID, specJSON, gatewayName); err != nil {
		result.Error = err
		return result
	}

	// Create deployment
	invokeURL, err := pm.deployGateway(ctx, apiID, gatewayName, config)
	if err != nil {
		result.Error = err
		return result
	}

	// Sync Lambda permissions
	if err := pm.addLambdaPermissionsForGateway(ctx, apiID, gatewayRoutes, lambdaARNs, config); err != nil {
		utils.Warn(ctx, "Failed to sync some Lambda permissions", map[string]interface{}{
			"gateway": gatewayName,
			"error":   err.Error(),
		})
	}

	result.ID = apiID
	result.URL = invokeURL
	result.Success = true

	utils.Info(ctx, "Successfully updated API Gateway via OpenAPI import", map[string]interface{}{
		"gateway": gatewayName,
		"api_id":  apiID,
	})

	return result
}

// putRestApiWithRetry calls PutRestApi with overwrite mode and retries on rate limiting.
func (pm *ParallelManager) putRestApiWithRetry(
	ctx context.Context,
	apiID string,
	specJSON []byte,
	gatewayName string,
) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		putOutput, err := pm.clients.ApiGateway.PutRestApi(ctx, &apigateway.PutRestApiInput{
			RestApiId:      aws.String(apiID),
			Body:           specJSON,
			Mode:           apigatewayTypes.PutModeOverwrite,
			FailOnWarnings: false,
		})
		if err == nil {
			utils.Info(ctx, "PutRestApi succeeded", map[string]interface{}{
				"gateway": gatewayName,
				"api_id":  apiID,
			})
			// Log any warnings from the response
			if putOutput != nil && len(putOutput.Warnings) > 0 {
				utils.Warn(ctx, "PutRestApi returned warnings", map[string]interface{}{
					"gateway":  gatewayName,
					"warnings": putOutput.Warnings,
				})
			}
			return nil
		}

		lastErr = err

		if strings.Contains(err.Error(), "TooManyRequests") || strings.Contains(err.Error(), "429") {
			backoff := time.Duration(attempt+1) * 2 * time.Second
			utils.Info(ctx, "PutRestApi rate limited, retrying...", map[string]interface{}{
				"gateway": gatewayName,
				"attempt": attempt + 1,
				"backoff": backoff.String(),
			})
			time.Sleep(backoff)
			continue
		}

		// Log the spec for debugging on non-retryable errors
		utils.Error(ctx, "PutRestApi failed", map[string]interface{}{
			"gateway":   gatewayName,
			"api_id":    apiID,
			"error":     err.Error(),
			"spec_size": len(specJSON),
		})
		return fmt.Errorf("PutRestApi failed for gateway '%s': %w", gatewayName, err)
	}

	return fmt.Errorf("PutRestApi failed for gateway '%s' after retries: %w", gatewayName, lastErr)
}

// deployGateway creates a deployment and returns the invoke URL.
func (pm *ParallelManager) deployGateway(
	ctx context.Context,
	apiID string,
	gatewayName string,
	config *DispatcherConfig,
) (string, error) {
	var deployErr error
	for attempt := 0; attempt < 5; attempt++ {
		_, deployErr = pm.clients.ApiGateway.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
			RestApiId: aws.String(apiID),
			StageName: aws.String(config.Environment),
		})
		if deployErr == nil {
			break
		}

		if strings.Contains(deployErr.Error(), "TooManyRequests") || strings.Contains(deployErr.Error(), "429") {
			backoff := time.Duration(attempt+1) * 2 * time.Second
			utils.Info(ctx, "Deployment rate limited, retrying...", map[string]interface{}{
				"gateway": gatewayName,
				"attempt": attempt + 1,
				"backoff": backoff.String(),
			})
			time.Sleep(backoff)
			continue
		}

		break
	}

	if deployErr != nil {
		return "", fmt.Errorf("failed to create deployment for gateway '%s': %w", gatewayName, deployErr)
	}

	invokeURL := fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com/%s", apiID, config.AwsRegion, config.Environment)

	utils.Info(ctx, "Created deployment via OpenAPI import", map[string]interface{}{
		"gateway":    gatewayName,
		"invoke_url": invokeURL,
	})

	return invokeURL, nil
}
