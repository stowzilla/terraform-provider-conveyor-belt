// internal/resources/api_gateway_operations.go
package resources

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigatewayTypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// ApiGatewayOperations handles all AWS API Gateway SDK operations.
type ApiGatewayOperations struct {
	client *apigateway.Client
	config *DispatcherConfig
}

// NewApiGatewayOperations creates a new ApiGatewayOperations instance.
func NewApiGatewayOperations(client *apigateway.Client, config *DispatcherConfig) *ApiGatewayOperations {
	return &ApiGatewayOperations{
		client: client,
		config: config,
	}
}

// ParsePathSegments parses a route path into segments for API Gateway resource creation.
// The path is used as-is without any prefix stripping. The gateway name is used separately
// for base path mappings on custom domains.
//
// Examples:
//   - ParsePathSegments("/waitlist") → ["waitlist"]
//   - ParsePathSegments("/ops/waitlist") → ["ops", "waitlist"]
//   - ParsePathSegments("/ops/{id}/items") → ["ops", "{id}", "items"]
//   - ParsePathSegments("/") → []
//
// Note: The gateway parameter is kept for backward compatibility but is no longer used
// for path manipulation. Base path mappings use the gateway name directly.
func ParsePathSegments(path string, gateway string) []string {
	// Parse path segments
	pathSegments := strings.Split(strings.Trim(path, "/"), "/")

	// If path is empty or just "/", return empty
	if len(pathSegments) == 0 || (len(pathSegments) == 1 && pathSegments[0] == "") {
		return []string{}
	}

	return pathSegments
}

// StripGatewayPrefix is deprecated - use ParsePathSegments instead.
// Kept for backward compatibility, now just calls ParsePathSegments.
func StripGatewayPrefix(path string, gateway string) []string {
	return ParsePathSegments(path, gateway)
}

// FindOrCreateRestAPI finds an existing API Gateway by name or creates a new one.
// This helps recover from failed deployments where API Gateways were created but not tracked in state.
func (ops *ApiGatewayOperations) FindOrCreateRestAPI(ctx context.Context, name, gateway string) (string, error) {
	utils.Info(ctx, "Looking for existing API Gateway or creating new one", map[string]interface{}{
		"name": name,
	})

	// First, check if an API Gateway with this name already exists
	listInput := &apigateway.GetRestApisInput{
		Limit: aws.Int32(500), // Get more results to find our API
	}

	listOutput, err := ops.client.GetRestApis(ctx, listInput)
	if err != nil {
		return "", fmt.Errorf("failed to list existing API Gateways: %w", err)
	}

	// Look for existing API Gateway with matching name
	for _, api := range listOutput.Items {
		if api.Name != nil && *api.Name == name {
			apiId := *api.Id
			utils.Info(ctx, "Found existing API Gateway - adopting it", map[string]interface{}{
				"gateway": gateway,
				"api_id":     apiId,
				"name":       name,
			})
			return apiId, nil
		}
	}

	// No existing API Gateway found, create a new one
	return ops.CreateRestAPI(ctx, name, gateway)
}

// CreateRestAPI creates a new REST API with retry logic for rate limiting.
func (ops *ApiGatewayOperations) CreateRestAPI(ctx context.Context, name, gateway string) (string, error) {
	utils.Info(ctx, "AWS connectivity verified, creating API Gateway", map[string]interface{}{
		"name": name,
	})

	createApiInput := &apigateway.CreateRestApiInput{
		Name:        aws.String(name),
		Description: aws.String(fmt.Sprintf("%s API Gateway", strings.Title(gateway))),
		EndpointConfiguration: &apigatewayTypes.EndpointConfiguration{
			Types: []apigatewayTypes.EndpointType{apigatewayTypes.EndpointTypeRegional},
		},
		Tags: buildResourceTags(ops.config, "APIGateway", name),
	}

	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		createApiOutput, err := ops.client.CreateRestApi(ctx, createApiInput)
		if err == nil {
			apiId := *createApiOutput.Id
			utils.Info(ctx, "Created API Gateway", map[string]interface{}{
				"gateway": gateway,
				"api_id":  apiId,
				"name":    name,
			})
			return apiId, nil
		}

		lastErr = err

		// Retry on rate limiting
		if strings.Contains(err.Error(), "TooManyRequests") || strings.Contains(err.Error(), "429") {
			backoff := time.Duration(attempt+1) * 2 * time.Second
			utils.Info(ctx, "API Gateway rate limited, retrying...", map[string]interface{}{
				"attempt": attempt + 1,
				"backoff": backoff.String(),
			})
			time.Sleep(backoff)
			continue
		}

		return "", err
	}

	return "", fmt.Errorf("failed to create API Gateway after retries: %w", lastErr)
}

// DeleteApiGateway deletes an API Gateway by ID.
func (ops *ApiGatewayOperations) DeleteApiGateway(ctx context.Context, apiId, name string) error {
	deleteInput := &apigateway.DeleteRestApiInput{
		RestApiId: aws.String(apiId),
	}

	_, err := ops.client.DeleteRestApi(ctx, deleteInput)
	if err != nil {
		return fmt.Errorf("failed to delete API Gateway %s (name: %s): %w", apiId, name, err)
	}

	return nil
}

// deleteApiGatewayWithRetry deletes an API Gateway with exponential backoff retry logic.
func (ops *ApiGatewayOperations) deleteApiGatewayWithRetry(ctx context.Context, apiId, name string, maxRetries int) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		deleteInput := &apigateway.DeleteRestApiInput{
			RestApiId: aws.String(apiId),
		}

		_, err := ops.client.DeleteRestApi(ctx, deleteInput)
		if err == nil {
			return nil // Success!
		}

		lastErr = err

		// Check if it's a rate limit error
		if strings.Contains(err.Error(), "TooManyRequestsException") || strings.Contains(err.Error(), "429") {
			if attempt < maxRetries {
				// Exponential backoff: 2s, 4s, 8s, 16s, 32s
				backoffSeconds := time.Duration(1<<uint(attempt)) * time.Second
				utils.Warn(ctx, fmt.Sprintf("Rate limited deleting API Gateway, retrying in %v (attempt %d/%d)", backoffSeconds, attempt, maxRetries), map[string]interface{}{
					"api_id": apiId,
					"name":   name,
				})
				time.Sleep(backoffSeconds)
				continue
			}
		} else {
			// For non-rate-limit errors, fail immediately
			return fmt.Errorf("failed to delete API Gateway %s (name: %s): %w", apiId, name, err)
		}
	}

	return fmt.Errorf("failed to delete API Gateway %s (name: %s) after %d retries: %w", apiId, name, maxRetries, lastErr)
}

// UpdateApiGatewayResources updates resources and methods for a gateway.
// This method now uses parallel processing internally for improved performance.
// lambdaFunctions is a map of lambda -> Lambda ARN (with actual function names including suffixes)
//
// Requirements: 6.1, 6.3, 6.4
func (ops *ApiGatewayOperations) UpdateApiGatewayResources(ctx context.Context, apiId string, routes []utils.Route, config *DispatcherConfig, lambdaFunctions map[string]string) error {
	// Use dedicated route processing concurrency helper
	concurrency := getRouteProcessingConcurrency(config)

	// Use parallel implementation
	result, err := ops.UpdateApiGatewayResourcesParallel(ctx, apiId, routes, config, lambdaFunctions, concurrency)
	if err != nil {
		return err
	}

	// If there were errors, return a summary error
	if result.HasErrors() {
		return fmt.Errorf("route update completed with %d errors: %s", result.FailureCount, result.ErrorSummary())
	}

	return nil
}

// CreateApiGatewayResources creates resources, methods, and integrations.
// Uses parallel processing internally for improved performance.
func (ops *ApiGatewayOperations) CreateApiGatewayResources(ctx context.Context, apiId string, routes []utils.Route, config *DispatcherConfig, lambdaFunctions map[string]string) error {
	concurrency := getRouteProcessingConcurrency(config)

	result, err := ops.CreateApiGatewayResourcesParallel(ctx, apiId, routes, config, lambdaFunctions, concurrency)
	if err != nil {
		return err
	}

	if result.HasErrors() {
		return fmt.Errorf("route processing completed with %d errors: %s", result.FailureCount, result.ErrorSummary())
	}

	return nil
}

// ApiGatewayExists checks if an API Gateway with the given ID exists in AWS.
func (ops *ApiGatewayOperations) ApiGatewayExists(ctx context.Context, apiId string) (bool, error) {
	input := &apigateway.GetRestApiInput{
		RestApiId: aws.String(apiId),
	}

	_, err := ops.client.GetRestApi(ctx, input)
	if err != nil {
		// Check if it's a "not found" error
		if strings.Contains(err.Error(), "NotFoundException") || strings.Contains(err.Error(), "NotFound") {
			return false, nil
		}
		// Other errors (permissions, network, etc.)
		return false, err
	}

	return true, nil
}

// CreateDeploymentAndStage creates deployment and stage.
func (ops *ApiGatewayOperations) CreateDeploymentAndStage(ctx context.Context, apiId string, config *DispatcherConfig, gateway string, logGroupArn string, apiGatewayName string) (string, error) {
	time.Sleep(200 * time.Millisecond)

	deploymentId, err := ops.createDeployment(ctx, apiId)
	if err != nil {
		return "", fmt.Errorf("failed to create deployment: %w", err)
	}

	return ops.createStage(ctx, apiId, deploymentId, config, gateway, logGroupArn, apiGatewayName)
}

// UpdateDeploymentAndStage creates a new deployment and updates the existing stage.
func (ops *ApiGatewayOperations) UpdateDeploymentAndStage(ctx context.Context, apiId string, config *DispatcherConfig, gateway string, logGroupArn string, apiGatewayName string) (string, error) {
	time.Sleep(200 * time.Millisecond)

	deploymentId, err := ops.createDeployment(ctx, apiId)
	if err != nil {
		return "", fmt.Errorf("failed to create deployment: %w", err)
	}

	return ops.updateStage(ctx, apiId, deploymentId, config, gateway, logGroupArn, apiGatewayName)
}

// createDeployment creates a deployment.
func (ops *ApiGatewayOperations) createDeployment(ctx context.Context, apiId string) (string, error) {
	// First, repair any methods missing integrations to prevent deployment failures
	if err := ops.repairMethodsWithoutIntegrations(ctx, apiId); err != nil {
		utils.Warn(ctx, "Failed to repair methods without integrations", map[string]interface{}{
			"api_id": apiId,
			"error":  err.Error(),
		})
		// Continue anyway - the deployment will fail with a clearer error if there are still issues
	}

	createDeploymentInput := &apigateway.CreateDeploymentInput{
		RestApiId:   aws.String(apiId),
		Description: aws.String("Deployment created by terraform-provider-conveyor-belt"),
	}

	// Retry deployment creation with backoff for rate limiting
	var createDeploymentOutput *apigateway.CreateDeploymentOutput
	var err error

	for attempt := 1; attempt <= 5; attempt++ {
		createDeploymentOutput, err = ops.client.CreateDeployment(ctx, createDeploymentInput)
		if err == nil {
			break
		}

		// Check if it's a rate limiting error
		if strings.Contains(err.Error(), "TooManyRequestsException") || strings.Contains(err.Error(), "429") {
			backoffDelay := time.Duration(attempt*attempt) * time.Second
			utils.Warn(ctx, "Rate limited on deployment creation, retrying", map[string]interface{}{
				"attempt": attempt,
				"delay":   backoffDelay.String(),
				"api_id":  apiId,
			})
			time.Sleep(backoffDelay)
			continue
		}

		// For other errors, fail immediately
		break
	}

	if err != nil {
		return "", fmt.Errorf("failed to create deployment after retries: %w", err)
	}

	return *createDeploymentOutput.Id, nil
}

// repairMethodsWithoutIntegrations scans all resources and deletes any methods that are missing integrations.
// This prevents "No integration defined for method" errors during deployment.
func (ops *ApiGatewayOperations) repairMethodsWithoutIntegrations(ctx context.Context, apiId string) error {
	utils.Info(ctx, "Scanning for methods without integrations", map[string]interface{}{
		"api_id": apiId,
	})

	// Get all resources
	allResources, err := ops.getAllResources(ctx, apiId)
	if err != nil {
		return fmt.Errorf("failed to get resources: %w", err)
	}

	repairCount := 0
	for _, resource := range allResources {
		if resource.ResourceMethods == nil {
			continue
		}

		for methodName := range resource.ResourceMethods {
			// Check if this method has an integration
			getMethodInput := &apigateway.GetMethodInput{
				RestApiId:  aws.String(apiId),
				ResourceId: resource.Id,
				HttpMethod: aws.String(methodName),
			}

			method, err := ops.client.GetMethod(ctx, getMethodInput)
			if err != nil {
				utils.Warn(ctx, "Failed to get method details", map[string]interface{}{
					"resource_id": *resource.Id,
					"path":        resource.Path,
					"method":      methodName,
					"error":       err.Error(),
				})
				continue
			}

			if method.MethodIntegration == nil {
				utils.Warn(ctx, "Found method without integration - deleting", map[string]interface{}{
					"resource_id": *resource.Id,
					"path":        resource.Path,
					"method":      methodName,
				})

				// Delete the incomplete method
				_, delErr := ops.client.DeleteMethod(ctx, &apigateway.DeleteMethodInput{
					RestApiId:  aws.String(apiId),
					ResourceId: resource.Id,
					HttpMethod: aws.String(methodName),
				})
				if delErr != nil {
					utils.Warn(ctx, "Failed to delete method without integration", map[string]interface{}{
						"resource_id": *resource.Id,
						"path":        resource.Path,
						"method":      methodName,
						"error":       delErr.Error(),
					})
				} else {
					repairCount++
				}
			}
		}
	}

	if repairCount > 0 {
		utils.Info(ctx, "Repaired methods without integrations", map[string]interface{}{
			"api_id":       apiId,
			"repair_count": repairCount,
		})
	}

	return nil
}

// createStage creates a stage with CloudWatch logging.
func (ops *ApiGatewayOperations) createStage(ctx context.Context, apiId, deploymentId string, config *DispatcherConfig, gateway string, logGroupArn string, apiGatewayName string) (string, error) {
	stageName := config.Environment

	// Check if stage already exists (orphan recovery)
	getStageInput := &apigateway.GetStageInput{
		RestApiId: aws.String(apiId),
		StageName: aws.String(stageName),
	}

	_, err := ops.client.GetStage(ctx, getStageInput)
	if err == nil {
		// Stage exists, update it instead of creating
		return ops.updateStage(ctx, apiId, deploymentId, config, gateway, logGroupArn, apiGatewayName)
	}

	createStageInput := &apigateway.CreateStageInput{
		RestApiId:    aws.String(apiId),
		StageName:    aws.String(stageName),
		DeploymentId: aws.String(deploymentId),
		Description:  aws.String(fmt.Sprintf("%s stage for %s API", config.Environment, gateway)),
	}

	_, err = ops.client.CreateStage(ctx, createStageInput)
	if err != nil {
		return "", fmt.Errorf("failed to create stage: %w", err)
	}

	// Update stage with CloudWatch logging configuration if log group ARN is available
	if logGroupArn != "" {
		logFormat := `{"requestId":"$context.requestId","ip":"$context.identity.sourceIp","caller":"$context.identity.caller","user":"$context.identity.user","requestTime":"$context.requestTime","httpMethod":"$context.httpMethod","resourcePath":"$context.resourcePath","status":"$context.status","protocol":"$context.protocol","responseLength":"$context.responseLength"}`

		updateStageInput := &apigateway.UpdateStageInput{
			RestApiId: aws.String(apiId),
			StageName: aws.String(stageName),
			PatchOperations: []apigatewayTypes.PatchOperation{
				{
					Op:    apigatewayTypes.OpReplace,
					Path:  aws.String("/accessLogSettings/destinationArn"),
					Value: aws.String(logGroupArn),
				},
				{
					Op:    apigatewayTypes.OpReplace,
					Path:  aws.String("/accessLogSettings/format"),
					Value: aws.String(logFormat),
				},
			},
		}

		_, err = ops.client.UpdateStage(ctx, updateStageInput)
		if err != nil {
			utils.Warn(ctx, "Failed to update stage with CloudWatch logging settings", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// Build stage URL
	stageUrl := fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com/%s",
		apiId, config.AwsRegion, stageName)

	return stageUrl, nil
}

// updateStage updates an existing stage with a new deployment.
func (ops *ApiGatewayOperations) updateStage(ctx context.Context, apiId, deploymentId string, config *DispatcherConfig, gateway string, logGroupArn string, apiGatewayName string) (string, error) {
	stageName := config.Environment

	// Update the stage to point to the new deployment
	updateStageInput := &apigateway.UpdateStageInput{
		RestApiId: aws.String(apiId),
		StageName: aws.String(stageName),
		PatchOperations: []apigatewayTypes.PatchOperation{
			{
				Op:    apigatewayTypes.OpReplace,
				Path:  aws.String("/deploymentId"),
				Value: aws.String(deploymentId),
			},
		},
	}

	// If we have a log group ARN, also update the access logging configuration
	if logGroupArn != "" {
		logFormat := `{"requestId":"$context.requestId","ip":"$context.identity.sourceIp","caller":"$context.identity.caller","user":"$context.identity.user","requestTime":"$context.requestTime","httpMethod":"$context.httpMethod","resourcePath":"$context.resourcePath","status":"$context.status","protocol":"$context.protocol","responseLength":"$context.responseLength"}`

		updateStageInput.PatchOperations = append(updateStageInput.PatchOperations,
			apigatewayTypes.PatchOperation{
				Op:    apigatewayTypes.OpReplace,
				Path:  aws.String("/accessLogSettings/destinationArn"),
				Value: aws.String(logGroupArn),
			},
			apigatewayTypes.PatchOperation{
				Op:    apigatewayTypes.OpReplace,
				Path:  aws.String("/accessLogSettings/format"),
				Value: aws.String(logFormat),
			},
		)
	}

	_, err := ops.client.UpdateStage(ctx, updateStageInput)
	if err != nil {
		return "", fmt.Errorf("failed to update stage: %w", err)
	}

	// Build stage URL
	stageUrl := fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com/%s",
		apiId, config.AwsRegion, stageName)

	return stageUrl, nil
}

// ConfigureGatewayResponses creates gateway responses with CORS headers for error responses.
// This is critical for CORS support when API Gateway returns errors before Lambda is invoked
// (e.g., Cognito authorizer failures return 401 without CORS headers, breaking browser clients).
//
// Gateway response types configured:
// - UNAUTHORIZED (401): Cognito/IAM auth failures
// - ACCESS_DENIED (403): Authorization policy denials
// - DEFAULT_4XX: All other 4xx errors
// - DEFAULT_5XX: All 5xx errors
// - MISSING_AUTHENTICATION_TOKEN (404): Route not found
// - RESOURCE_NOT_FOUND (404): Wrong HTTP method
// - BAD_REQUEST_BODY (400): Invalid JSON
func (ops *ApiGatewayOperations) ConfigureGatewayResponses(ctx context.Context, apiId string, config *DispatcherConfig) error {
	utils.Info(ctx, "Configuring gateway responses with CORS headers", map[string]interface{}{
		"api_id":          apiId,
		"friendly_errors": config.FriendlyErrors,
	})

	frontendUrl := GetCORSOriginForConfig(config)

	// Response types that need CORS headers
	responseTypes := []apigatewayTypes.GatewayResponseType{
		apigatewayTypes.GatewayResponseTypeUnauthorized,
		apigatewayTypes.GatewayResponseTypeAccessDenied,
		apigatewayTypes.GatewayResponseTypeDefault4xx,
		apigatewayTypes.GatewayResponseTypeDefault5xx,
		apigatewayTypes.GatewayResponseTypeMissingAuthenticationToken,
		apigatewayTypes.GatewayResponseTypeResourceNotFound,
		apigatewayTypes.GatewayResponseTypeBadRequestBody,
	}

	// CORS headers to add to all error responses
	responseParameters := map[string]string{
		"gatewayresponse.header.Access-Control-Allow-Origin":  fmt.Sprintf("'%s'", frontendUrl),
		"gatewayresponse.header.Access-Control-Allow-Headers": "'Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token'",
		"gatewayresponse.header.Access-Control-Allow-Methods": "'GET,POST,PUT,DELETE,PATCH,OPTIONS'",
	}

	for _, responseType := range responseTypes {
		// Get the appropriate response template based on friendly_errors setting
		responseTemplate := ops.getResponseTemplate(responseType, config)

		input := &apigateway.PutGatewayResponseInput{
			RestApiId:          aws.String(apiId),
			ResponseType:       responseType,
			ResponseParameters: responseParameters,
		}

		// Add response template if we have one
		if responseTemplate != "" {
			input.ResponseTemplates = map[string]string{
				"application/json": responseTemplate,
			}
		}

		// Override status code for MISSING_AUTHENTICATION_TOKEN
		// Use 200 for OPTIONS (CORS preflight) to work, 404 for other methods
		// Note: API Gateway doesn't support conditional status codes, so we use 404
		// but ensure CORS headers are present so browsers can see the error
		if responseType == apigatewayTypes.GatewayResponseTypeMissingAuthenticationToken {
			input.StatusCode = aws.String("404")
		}

		_, err := ops.client.PutGatewayResponse(ctx, input)
		if err != nil {
			utils.Warn(ctx, "Failed to configure gateway response", map[string]interface{}{
				"api_id":        apiId,
				"response_type": string(responseType),
				"error":         err.Error(),
			})
			// Continue with other response types - partial success is better than none
		} else {
			utils.Info(ctx, "Configured gateway response with CORS headers", map[string]interface{}{
				"api_id":        apiId,
				"response_type": string(responseType),
			})
		}
	}

	return nil
}

// getResponseTemplate returns the appropriate response template based on response type and config
func (ops *ApiGatewayOperations) getResponseTemplate(responseType apigatewayTypes.GatewayResponseType, config *DispatcherConfig) string {
	if config.FriendlyErrors {
		return ops.getFriendlyErrorTemplate(responseType, config.Environment)
	}
	return ops.getProductionErrorTemplate(responseType)
}

// getFriendlyErrorTemplate returns detailed error messages for development environments
func (ops *ApiGatewayOperations) getFriendlyErrorTemplate(responseType apigatewayTypes.GatewayResponseType, environment string) string {
	switch responseType {
	case apigatewayTypes.GatewayResponseTypeMissingAuthenticationToken:
		return fmt.Sprintf(`{
  "error": "Route Not Found",
  "message": "The API route you are trying to access does not exist. Check the HTTP method (GET/POST/PUT/DELETE) and path.",
  "path": "$context.resourcePath",
  "method": "$context.httpMethod",
  "environment": "%s",
  "hint": "Run ./scripts/list_routes.rb to see all available routes"
}`, environment)

	case apigatewayTypes.GatewayResponseTypeResourceNotFound:
		return fmt.Sprintf(`{
  "error": "Method Not Allowed",
  "message": "The HTTP method is not allowed for this resource.",
  "path": "$context.resourcePath",
  "method": "$context.httpMethod",
  "environment": "%s",
  "hint": "This route exists but doesn't support $context.httpMethod. Try a different HTTP method (GET, POST, PUT, DELETE, or OPTIONS)."
}`, environment)

	case apigatewayTypes.GatewayResponseTypeBadRequestBody:
		return fmt.Sprintf(`{
  "error": "Bad Request",
  "message": "The request body is invalid or malformed JSON.",
  "details": $context.error.messageString,
  "environment": "%s"
}`, environment)

	case apigatewayTypes.GatewayResponseTypeUnauthorized:
		return fmt.Sprintf(`{
  "error": "Unauthorized",
  "message": "Authentication failed. Check your authorization token.",
  "details": $context.error.messageString,
  "environment": "%s"
}`, environment)

	case apigatewayTypes.GatewayResponseTypeAccessDenied:
		return fmt.Sprintf(`{
  "error": "Access Denied",
  "message": "You don't have permission to access this resource.",
  "details": $context.error.messageString,
  "environment": "%s"
}`, environment)

	case apigatewayTypes.GatewayResponseTypeDefault4xx:
		return fmt.Sprintf(`{
  "error": "Client Error",
  "message": "The request could not be processed. This may be due to an unsupported HTTP method, invalid headers, or malformed request.",
  "path": "$context.resourcePath",
  "method": "$context.httpMethod",
  "environment": "%s",
  "hint": "Check that you're using the correct HTTP method (GET, POST, PUT, DELETE) for this route. If the route exists, try a different method."
}`, environment)

	default:
		// For other error types, return a generic friendly message
		return fmt.Sprintf(`{
  "error": "$context.error.responseType",
  "message": $context.error.messageString,
  "environment": "%s"
}`, environment)
	}
}

// getProductionErrorTemplate returns minimal error messages for production environments
func (ops *ApiGatewayOperations) getProductionErrorTemplate(responseType apigatewayTypes.GatewayResponseType) string {
	switch responseType {
	case apigatewayTypes.GatewayResponseTypeMissingAuthenticationToken,
		apigatewayTypes.GatewayResponseTypeResourceNotFound:
		return `{
  "error": "Not Found",
  "message": "The requested resource does not exist"
}`

	case apigatewayTypes.GatewayResponseTypeBadRequestBody:
		return `{
  "error": "Bad Request",
  "message": "Invalid request body"
}`

	case apigatewayTypes.GatewayResponseTypeUnauthorized:
		return `{
  "error": "Unauthorized",
  "message": "Authentication required"
}`

	case apigatewayTypes.GatewayResponseTypeAccessDenied:
		return `{
  "error": "Forbidden",
  "message": "Access denied"
}`

	case apigatewayTypes.GatewayResponseTypeDefault4xx:
		return `{
  "error": "Bad Request",
  "message": "The request could not be processed"
}`

	default:
		// For other error types, return generic production message
		return `{
  "error": "Error",
  "message": "An error occurred processing your request"
}`
	}
}

// getAllResources fetches all resources with pagination support
func (ops *ApiGatewayOperations) getAllResources(ctx context.Context, apiId string) ([]apigatewayTypes.Resource, error) {
	var allResources []apigatewayTypes.Resource
	var position *string

	for {
		getResourcesInput := &apigateway.GetResourcesInput{
			RestApiId: aws.String(apiId),
			Limit:     aws.Int32(500),
			Position:  position,
		}

		getResourcesOutput, err := ops.client.GetResources(ctx, getResourcesInput)
		if err != nil {
			return nil, err
		}

		allResources = append(allResources, getResourcesOutput.Items...)

		if getResourcesOutput.Position == nil || *getResourcesOutput.Position == "" {
			break
		}
		position = getResourcesOutput.Position
	}

	return allResources, nil
}

// CreateApiGatewayResourcesParallel creates resources, methods, and integrations in parallel.
// This is the new parallel implementation that uses ParallelRouteProcessor.
// lambdaFunctions is a map of lambda -> Lambda ARN (with actual function names including suffixes)
//
// Requirements: 1.1, 6.1
func (ops *ApiGatewayOperations) CreateApiGatewayResourcesParallel(ctx context.Context, apiId string, routes []utils.Route, config *DispatcherConfig, lambdaFunctions map[string]string, concurrency int) (*RouteProcessingResult, error) {
	utils.Info(ctx, "Creating API Gateway resources using parallel processing", map[string]interface{}{
		"api_id":      apiId,
		"route_count": len(routes),
		"concurrency": concurrency,
	})

	// Create parallel route processor
	processor := NewParallelRouteProcessor(ops.client, config, concurrency)

	// Process routes in parallel
	result, err := processor.ProcessRoutes(ctx, apiId, routes, lambdaFunctions)
	if err != nil {
		return nil, fmt.Errorf("parallel route processing failed: %w", err)
	}

	// Log summary
	if result.HasErrors() {
		utils.Warn(ctx, "Parallel route processing completed with errors", map[string]interface{}{
			"api_id":        apiId,
			"success_count": result.SuccessCount,
			"failure_count": result.FailureCount,
			"duration_ms":   result.TotalDuration.Milliseconds(),
		})
	} else {
		utils.Info(ctx, "Parallel route processing completed successfully", map[string]interface{}{
			"api_id":        apiId,
			"success_count": result.SuccessCount,
			"duration_ms":   result.TotalDuration.Milliseconds(),
		})
	}

	return result, nil
}

// UpdateApiGatewayResourcesParallel updates resources and methods for a gateway using parallel processing.
// This handles cleanup of unused resources and then creates/updates routes in parallel.
// lambdaFunctions is a map of lambda -> Lambda ARN (with actual function names including suffixes)
//
// Requirements: 1.2
func (ops *ApiGatewayOperations) UpdateApiGatewayResourcesParallel(ctx context.Context, apiId string, routes []utils.Route, config *DispatcherConfig, lambdaFunctions map[string]string, concurrency int) (*RouteProcessingResult, error) {
	utils.Info(ctx, "Updating API Gateway resources using parallel processing (with cleanup)", map[string]interface{}{
		"api_id":      apiId,
		"route_count": len(routes),
		"concurrency": concurrency,
	})

	// Step 1: Get all existing resources
	existingResources := make(map[string]apigatewayTypes.Resource) // path -> resource
	var position *string

	for {
		getResourcesInput := &apigateway.GetResourcesInput{
			RestApiId: aws.String(apiId),
			Limit:     aws.Int32(500),
			Position:  position,
		}
		getResourcesOutput, err := ops.client.GetResources(ctx, getResourcesInput)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing resources: %w", err)
		}

		for _, resource := range getResourcesOutput.Items {
			if resource.Path != nil {
				existingResources[*resource.Path] = resource
			}
		}

		if getResourcesOutput.Position == nil {
			break
		}
		position = getResourcesOutput.Position
	}

	utils.Info(ctx, "Found existing resources", map[string]interface{}{
		"count": len(existingResources),
	})

	// Step 2: Build set of desired paths and methods from routes
	desiredPaths := make(map[string]bool)
	desiredMethods := make(map[string]bool) // "path:METHOD" -> true

	for _, route := range routes {
		pathSegments := StripGatewayPrefix(route.Path, route.Gateway)

		// Build the full path for this route
		currentPath := ""
		for _, segment := range pathSegments {
			currentPath = currentPath + "/" + segment
			desiredPaths[currentPath] = true
		}

		// If no segments, it's the root path
		if len(pathSegments) == 0 {
			currentPath = "/"
		}

		// Mark the method as desired
		methodKey := fmt.Sprintf("%s:%s", currentPath, route.Verb)
		desiredMethods[methodKey] = true
		// Also mark OPTIONS as desired for CORS
		optionsKey := fmt.Sprintf("%s:OPTIONS", currentPath)
		desiredMethods[optionsKey] = true
	}

	// Always keep root path
	desiredPaths["/"] = true

	utils.Info(ctx, "Desired paths and methods", map[string]interface{}{
		"paths":   len(desiredPaths),
		"methods": len(desiredMethods),
	})

	// Step 3: Delete methods that are no longer needed
	for path, resource := range existingResources {
		if resource.ResourceMethods != nil {
			for method := range resource.ResourceMethods {
				methodKey := fmt.Sprintf("%s:%s", path, method)
				if !desiredMethods[methodKey] && path != "/" {
					utils.Info(ctx, "Deleting unused method", map[string]interface{}{
						"path":   path,
						"method": method,
					})
					_, err := ops.client.DeleteMethod(ctx, &apigateway.DeleteMethodInput{
						RestApiId:  aws.String(apiId),
						ResourceId: resource.Id,
						HttpMethod: aws.String(method),
					})
					if err != nil {
						utils.Warn(ctx, "Failed to delete method", map[string]interface{}{
							"path":   path,
							"method": method,
							"error":  err.Error(),
						})
					}
				}
			}
		}
	}

	// Step 4: Delete resources that are no longer needed (in reverse order by path depth)
	pathsToDelete := make([]string, 0)
	for path := range existingResources {
		if !desiredPaths[path] && path != "/" {
			pathsToDelete = append(pathsToDelete, path)
		}
	}
	// Sort by depth (more slashes = deeper = delete first)
	sort.Slice(pathsToDelete, func(i, j int) bool {
		return strings.Count(pathsToDelete[i], "/") > strings.Count(pathsToDelete[j], "/")
	})

	for _, path := range pathsToDelete {
		resource := existingResources[path]
		utils.Info(ctx, "Deleting unused resource", map[string]interface{}{
			"path": path,
		})
		_, err := ops.client.DeleteResource(ctx, &apigateway.DeleteResourceInput{
			RestApiId:  aws.String(apiId),
			ResourceId: resource.Id,
		})
		if err != nil {
			utils.Warn(ctx, "Failed to delete resource", map[string]interface{}{
				"path":  path,
				"error": err.Error(),
			})
		}
	}

	// Step 5: Use parallel processing to create/update all desired resources
	return ops.CreateApiGatewayResourcesParallel(ctx, apiId, routes, config, lambdaFunctions, concurrency)
}