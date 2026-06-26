// internal/resources/gateway_resource.go
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &gatewayResource{}
	_ resource.ResourceWithConfigure   = &gatewayResource{}
	_ resource.ResourceWithImportState = &gatewayResource{}
)

// NewGatewayResource is a helper function to simplify the provider implementation.
func NewGatewayResource() resource.Resource {
	return &gatewayResource{}
}

// gatewayResource is the resource implementation for individual API Gateways.
type gatewayResource struct {
	providerConfig    *DispatcherConfig
	clients           *ResourceClients
	apiGatewayOps     *ApiGatewayOperations
	cloudWatchManager *CloudWatchManager
}

// GatewayResourceModel describes the resource data model.
type GatewayResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	AppName             types.String `tfsdk:"app_name"`
	Routes              types.List   `tfsdk:"routes"`
	FrontendUrls        types.List   `tfsdk:"frontend_urls"`
	CognitoUserPoolArns types.List   `tfsdk:"cognito_user_pool_arns"`
	FriendlyErrors      types.Bool   `tfsdk:"friendly_errors"`
	Tags                types.Map    `tfsdk:"tags"`
	// Computed outputs
	ApiId      types.String `tfsdk:"api_id"`
	InvokeUrl  types.String `tfsdk:"invoke_url"`
	RoutesHash types.String `tfsdk:"routes_hash"`
}

// RouteModel represents a single route in the gateway.
type RouteModel struct {
	Name      types.String `tfsdk:"name"`
	Verb      types.String `tfsdk:"verb"`
	Path      types.String `tfsdk:"path"`
	LambdaArn types.String `tfsdk:"lambda_arn"`
	Auth      types.String `tfsdk:"auth"`
}

// Metadata returns the resource type name.
func (r *gatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway"
}


// Schema defines the schema for the resource.
func (r *gatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single API Gateway with its routes and integrations.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Resource identifier",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Gateway name (without app/env prefix)",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"app_name": schema.StringAttribute{
				Description: "Application name for resource naming",
				Required:    true,
			},
			"routes": schema.ListNestedAttribute{
				Description: "Routes for this gateway",
				Required:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "Route name identifier",
							Required:    true,
						},
						"verb": schema.StringAttribute{
							Description: "HTTP method (GET, POST, PUT, DELETE, etc.)",
							Required:    true,
						},
						"path": schema.StringAttribute{
							Description: "API path (e.g., /users/{id})",
							Required:    true,
						},
						"lambda_arn": schema.StringAttribute{
							Description: "ARN of Lambda function to invoke",
							Required:    true,
						},
						"auth": schema.StringAttribute{
							Description: "Authorization type (none, cognito, iam)",
							Optional:    true,
						},
					},
				},
			},
			"frontend_urls": schema.ListAttribute{
				Description: "Frontend URLs for CORS configuration",
				Required:    true,
				ElementType: types.StringType,
			},
			"cognito_user_pool_arns": schema.ListAttribute{
				Description: "Cognito User Pool ARNs for authentication",
				Optional:    true,
				ElementType: types.StringType,
			},
			"friendly_errors": schema.BoolAttribute{
				Description: "Enable friendly error messages for missing routes. " +
					"When true, API Gateway returns detailed debugging information for 404 errors. " +
					"Recommended: set to true for dev/uat/staging, false for production. Default: false.",
				Optional: true,
			},
			"tags": schema.MapAttribute{
				Description: "Tags for this Gateway's resources",
				Optional:    true,
				ElementType: types.StringType,
			},
			// Computed outputs
			"api_id": schema.StringAttribute{
				Description: "API Gateway REST API ID",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"invoke_url": schema.StringAttribute{
				Description: "API Gateway invoke URL",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"routes_hash": schema.StringAttribute{
				Description: "Hash of routes for change detection",
				Computed:    true,
			},
		},
	}
}


// Configure adds the provider configured client to the resource.
func (r *gatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*DispatcherClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *DispatcherClient, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	// Store the provider configuration including defaults for later use
	r.providerConfig = &DispatcherConfig{
		Environment: client.Environment,
		AwsRegion:   client.AwsRegion,
		DefaultTags: client.DefaultTags,
	}
}

// initializeManagers initializes AWS clients and managers for the resource
func (r *gatewayResource) initializeManagers(ctx context.Context, config *DispatcherConfig) error {
	if r.clients != nil {
		return nil // Already initialized
	}

	clients, err := initializeClients(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to initialize AWS clients: %w", err)
	}

	r.clients = clients
	r.apiGatewayOps = NewApiGatewayOperations(clients.ApiGateway, config)
	r.cloudWatchManager = NewCloudWatchManager(clients.CloudWatchLogs, clients.CloudWatch, config)

	return nil
}

// buildConfigFromModel builds a DispatcherConfig from the resource model
func (r *gatewayResource) buildConfigFromModel(ctx context.Context, model *GatewayResourceModel) (*DispatcherConfig, error) {
	config := &DispatcherConfig{
		Environment: r.providerConfig.Environment,
		AwsRegion:   r.providerConfig.AwsRegion,
		AppName:     model.AppName.ValueString(),
	}

	// Get AWS account ID
	accountId, err := getAwsAccountId(ctx, config.AwsRegion)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS account ID: %w", err)
	}
	config.AwsAccountId = accountId

	// Extract frontend URLs
	if !model.FrontendUrls.IsNull() && !model.FrontendUrls.IsUnknown() {
		var frontendUrls []string
		diags := model.FrontendUrls.ElementsAs(ctx, &frontendUrls, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract frontend_urls")
		}
		config.FrontendUrls = frontendUrls
	}

	// Extract Cognito User Pool ARNs
	if !model.CognitoUserPoolArns.IsNull() && !model.CognitoUserPoolArns.IsUnknown() {
		var cognitoArns []string
		diags := model.CognitoUserPoolArns.ElementsAs(ctx, &cognitoArns, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract cognito_user_pool_arns")
		}
		config.CognitoUserPoolArns = cognitoArns
	}

	// Extract tags with configuration precedence (resource tags override provider default tags)
	config.Tags = r.getEffectiveTags(ctx, model)

	// Extract friendly_errors setting (defaults to false)
	if !model.FriendlyErrors.IsNull() && !model.FriendlyErrors.IsUnknown() {
		config.FriendlyErrors = model.FriendlyErrors.ValueBool()
	}

	return config, nil
}

// getEffectiveTags returns the effective tags with precedence:
// Resource-level tags override provider-level default tags
func (r *gatewayResource) getEffectiveTags(ctx context.Context, model *GatewayResourceModel) map[string]string {
	effectiveTags := make(map[string]string)
	
	// Start with provider default tags
	if r.providerConfig != nil && r.providerConfig.DefaultTags != nil {
		for k, v := range r.providerConfig.DefaultTags {
			effectiveTags[k] = v
		}
	}
	
	// Override with resource-level tags
	if !model.Tags.IsNull() && !model.Tags.IsUnknown() {
		resourceTags := make(map[string]string)
		diags := model.Tags.ElementsAs(ctx, &resourceTags, false)
		if !diags.HasError() {
			for k, v := range resourceTags {
				effectiveTags[k] = v
			}
		}
	}
	
	return effectiveTags
}

// extractRoutesFromModel extracts routes from the model and converts to utils.Route format
func (r *gatewayResource) extractRoutesFromModel(ctx context.Context, model *GatewayResourceModel) ([]utils.Route, map[string]string, error) {
	if model.Routes.IsNull() || model.Routes.IsUnknown() {
		return nil, nil, fmt.Errorf("routes cannot be null or unknown")
	}

	var routeModels []RouteModel
	diags := model.Routes.ElementsAs(ctx, &routeModels, false)
	if diags.HasError() {
		return nil, nil, fmt.Errorf("failed to extract routes from model")
	}

	routes := make([]utils.Route, len(routeModels))
	lambdaFunctions := make(map[string]string) // lambda name -> ARN

	gatewayName := model.Name.ValueString()

	for i, rm := range routeModels {
		auth := rm.Auth.ValueString()
		if auth == "" {
			auth = "none"
		}

		// Extract lambda name from ARN for the lambdaFunctions map
		lambdaArn := rm.LambdaArn.ValueString()
		lambdaName := extractLambdaNameFromArn(lambdaArn)

		routes[i] = utils.Route{
			Name:    rm.Name.ValueString(),
			Verb:    rm.Verb.ValueString(),
			Path:    rm.Path.ValueString(),
			Gateway: gatewayName,
			Lambda:  lambdaName,
			Auth:    auth,
		}

		// Store the ARN mapping
		lambdaFunctions[lambdaName] = lambdaArn
	}

	return routes, lambdaFunctions, nil
}

// extractLambdaNameFromArn extracts the function name from a Lambda ARN
func extractLambdaNameFromArn(arn string) string {
	// ARN format: arn:aws:lambda:region:account:function:name
	if strings.HasPrefix(arn, "arn:aws:lambda:") {
		parts := strings.Split(arn, ":")
		if len(parts) >= 7 {
			return parts[6]
		}
	}
	// If not a valid ARN, return as-is (might be just a function name)
	return arn
}


// Create creates the resource and sets the initial Terraform state.
func (r *gatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build configuration from model
	config, err := r.buildConfigFromModel(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to build configuration",
			"Could not build configuration from resource attributes: "+err.Error(),
		)
		return
	}

	// Initialize AWS clients and managers
	if err := r.initializeManagers(ctx, config); err != nil {
		resp.Diagnostics.AddError(
			"Failed to initialize AWS clients",
			"Could not initialize AWS clients: "+err.Error(),
		)
		return
	}

	gatewayName := plan.Name.ValueString()
	apiGatewayName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, gatewayName)

	utils.Info(ctx, "Creating Gateway resource", map[string]interface{}{
		"name":             gatewayName,
		"api_gateway_name": apiGatewayName,
	})

	// Extract routes from model
	routes, lambdaFunctions, err := r.extractRoutesFromModel(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to extract routes",
			"Could not extract routes from resource attributes: "+err.Error(),
		)
		return
	}

	// Step 1: Create CloudWatch log group for API Gateway
	utils.Info(ctx, "Creating CloudWatch log group for API Gateway...")
	logGroupArn, err := r.cloudWatchManager.CreateApiGatewayLogGroup(ctx, apiGatewayName)
	if err != nil {
		utils.Warn(ctx, "Failed to create CloudWatch log group for API Gateway", map[string]interface{}{
			"api_gateway_name": apiGatewayName,
			"error":            err.Error(),
		})
		// Continue anyway - logging is optional
	}

	// Step 2: Create or find REST API
	utils.Info(ctx, "Creating REST API...")
	apiId, err := r.apiGatewayOps.FindOrCreateRestAPI(ctx, apiGatewayName, gatewayName)
	if err != nil {
		// Rollback: delete log group
		r.cloudWatchManager.DeleteApiGatewayLogGroup(ctx, apiGatewayName)

		resp.Diagnostics.AddError(
			"Failed to create REST API",
			"Could not create API Gateway REST API: "+err.Error(),
		)
		return
	}

	// Step 3: Configure gateway responses with CORS headers for error responses
	// This must be done before creating routes to ensure CORS works for auth failures
	utils.Info(ctx, "Configuring gateway responses with CORS headers...")
	if err := r.apiGatewayOps.ConfigureGatewayResponses(ctx, apiId, config); err != nil {
		utils.Warn(ctx, "Failed to configure gateway responses", map[string]interface{}{
			"api_gateway_name": apiGatewayName,
			"error":            err.Error(),
		})
		// Continue anyway - gateway responses are important but not critical for basic functionality
	}

	// Step 4: Create resources, methods, and integrations for each route
	utils.Info(ctx, "Creating API Gateway resources and methods...")
	if err := r.apiGatewayOps.CreateApiGatewayResources(ctx, apiId, routes, config, lambdaFunctions); err != nil {
		// Rollback: delete API Gateway and log group
		r.apiGatewayOps.DeleteApiGateway(ctx, apiId, apiGatewayName)
		r.cloudWatchManager.DeleteApiGatewayLogGroup(ctx, apiGatewayName)

		resp.Diagnostics.AddError(
			"Failed to create API Gateway resources",
			"Could not create API Gateway resources and methods: "+err.Error(),
		)
		return
	}

	// Step 5: Create deployment and stage
	utils.Info(ctx, "Creating deployment and stage...")
	invokeUrl, err := r.apiGatewayOps.CreateDeploymentAndStage(ctx, apiId, config, gatewayName, logGroupArn, apiGatewayName)
	if err != nil {
		// Rollback: delete API Gateway and log group
		r.apiGatewayOps.DeleteApiGateway(ctx, apiId, apiGatewayName)
		r.cloudWatchManager.DeleteApiGatewayLogGroup(ctx, apiGatewayName)

		resp.Diagnostics.AddError(
			"Failed to create deployment",
			"Could not create API Gateway deployment and stage: "+err.Error(),
		)
		return
	}

	// Step 6: Add Lambda permissions for API Gateway to invoke Lambda functions
	utils.Info(ctx, "Adding Lambda invoke permissions...")
	if err := r.addLambdaPermissions(ctx, apiId, routes, lambdaFunctions, config); err != nil {
		utils.Warn(ctx, "Failed to add some Lambda permissions", map[string]interface{}{
			"error": err.Error(),
		})
		// Continue anyway - permissions might already exist
	}

	// Calculate routes hash for change detection
	routesHash := r.calculateRoutesHash(routes)

	// Populate computed outputs
	plan.ID = types.StringValue(apiId)
	plan.ApiId = types.StringValue(apiId)
	plan.InvokeUrl = types.StringValue(invokeUrl)
	plan.RoutesHash = types.StringValue(routesHash)

	utils.Info(ctx, "Successfully created Gateway resource", map[string]interface{}{
		"api_gateway_name": apiGatewayName,
		"api_id":           apiId,
		"invoke_url":       invokeUrl,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// addLambdaPermissions adds permissions for API Gateway to invoke Lambda functions
func (r *gatewayResource) addLambdaPermissions(ctx context.Context, apiId string, routes []utils.Route, lambdaFunctions map[string]string, config *DispatcherConfig) error {
	// Track which Lambda functions we've already added permissions for
	addedPermissions := make(map[string]bool)

	for _, route := range routes {
		lambdaArn, exists := lambdaFunctions[route.Lambda]
		if !exists {
			continue
		}

		// Skip if we've already added permission for this Lambda
		if addedPermissions[lambdaArn] {
			continue
		}

		// Extract function name from ARN
		functionName := extractLambdaNameFromArn(lambdaArn)

		// Create a unique statement ID for this permission
		statementId := fmt.Sprintf("apigateway-%s-%s", apiId, strings.ReplaceAll(functionName, "-", ""))

		// Source ARN for API Gateway
		sourceArn := fmt.Sprintf("arn:aws:execute-api:%s:%s:%s/*/*/*",
			config.AwsRegion, config.AwsAccountId, apiId)

		// Add permission using Lambda client
		_, err := r.clients.Lambda.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(statementId),
			Action:       aws.String("lambda:InvokeFunction"),
			Principal:    aws.String("apigateway.amazonaws.com"),
			SourceArn:    aws.String(sourceArn),
		})

		if err != nil {
			// Ignore if permission already exists
			if !strings.Contains(err.Error(), "ResourceConflictException") {
				utils.Warn(ctx, "Failed to add Lambda permission", map[string]interface{}{
					"function": functionName,
					"error":    err.Error(),
				})
			}
		}

		addedPermissions[lambdaArn] = true
	}

	return nil
}

// calculateRoutesHash calculates a hash of the routes for change detection
func (r *gatewayResource) calculateRoutesHash(routes []utils.Route) string {
	// Sort routes for deterministic hashing
	sortedRoutes := make([]utils.Route, len(routes))
	copy(sortedRoutes, routes)
	utils.SortRoutes(sortedRoutes)

	// Create a simplified structure for hashing
	type routeForHash struct {
		Name   string
		Verb   string
		Path   string
		Lambda string
		Auth   string
	}

	hashRoutes := make([]routeForHash, len(sortedRoutes))
	for i, r := range sortedRoutes {
		hashRoutes[i] = routeForHash{
			Name:   r.Name,
			Verb:   r.Verb,
			Path:   r.Path,
			Lambda: r.Lambda,
			Auth:   r.Auth,
		}
	}

	// Sort by multiple fields for determinism
	sort.Slice(hashRoutes, func(i, j int) bool {
		if hashRoutes[i].Path != hashRoutes[j].Path {
			return hashRoutes[i].Path < hashRoutes[j].Path
		}
		if hashRoutes[i].Verb != hashRoutes[j].Verb {
			return hashRoutes[i].Verb < hashRoutes[j].Verb
		}
		return hashRoutes[i].Name < hashRoutes[j].Name
	})

	routesJSON, err := json.Marshal(hashRoutes)
	if err != nil {
		return ""
	}

	hasher := sha256.New()
	hasher.Write(routesJSON)
	return hex.EncodeToString(hasher.Sum(nil))
}


// Read reads the resource state from AWS.
func (r *gatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build configuration from model
	config, err := r.buildConfigFromModel(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to build configuration",
			"Could not build configuration from resource attributes: "+err.Error(),
		)
		return
	}

	// Initialize AWS clients and managers
	if err := r.initializeManagers(ctx, config); err != nil {
		resp.Diagnostics.AddError(
			"Failed to initialize AWS clients",
			"Could not initialize AWS clients: "+err.Error(),
		)
		return
	}

	apiId := state.ApiId.ValueString()
	if apiId == "" {
		apiId = state.ID.ValueString()
	}

	utils.Info(ctx, "Reading Gateway resource", map[string]interface{}{
		"api_id": apiId,
	})

	// Check if API Gateway exists
	exists, err := r.apiGatewayOps.ApiGatewayExists(ctx, apiId)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to check API Gateway existence",
			"Could not check if API Gateway exists: "+err.Error(),
		)
		return
	}

	if !exists {
		// Resource no longer exists
		resp.State.RemoveResource(ctx)
		return
	}

	// Read REST API details
	getApiInput := &apigateway.GetRestApiInput{
		RestApiId: aws.String(apiId),
	}

	apiOutput, err := r.clients.ApiGateway.GetRestApi(ctx, getApiInput)
	if err != nil {
		if strings.Contains(err.Error(), "NotFoundException") {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Failed to read REST API",
			"Could not read API Gateway REST API: "+err.Error(),
		)
		return
	}

	// Update state with current values from AWS
	state.ApiId = types.StringValue(apiId)

	// Build invoke URL using the environment as the stage name
	invokeUrl := fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com/%s",
		apiId, config.AwsRegion, config.Environment)
	state.InvokeUrl = types.StringValue(invokeUrl)

	// Recalculate routes hash from current routes in state
	routes, _, err := r.extractRoutesFromModel(ctx, &state)
	if err == nil {
		state.RoutesHash = types.StringValue(r.calculateRoutesHash(routes))
	}

	utils.Info(ctx, "Successfully read Gateway resource", map[string]interface{}{
		"api_id":     apiId,
		"api_name":   *apiOutput.Name,
		"invoke_url": invokeUrl,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}


// Update updates the resource and sets the updated Terraform state.
func (r *gatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan GatewayResourceModel
	var state GatewayResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build configuration from plan
	config, err := r.buildConfigFromModel(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to build configuration",
			"Could not build configuration from resource attributes: "+err.Error(),
		)
		return
	}

	// Initialize AWS clients and managers
	if err := r.initializeManagers(ctx, config); err != nil {
		resp.Diagnostics.AddError(
			"Failed to initialize AWS clients",
			"Could not initialize AWS clients: "+err.Error(),
		)
		return
	}

	apiId := state.ApiId.ValueString()
	gatewayName := plan.Name.ValueString()
	apiGatewayName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, gatewayName)

	utils.Info(ctx, "Updating Gateway resource", map[string]interface{}{
		"api_id":           apiId,
		"api_gateway_name": apiGatewayName,
	})

	// Extract routes from plan
	newRoutes, lambdaFunctions, err := r.extractRoutesFromModel(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to extract routes",
			"Could not extract routes from resource attributes: "+err.Error(),
		)
		return
	}

	// Calculate new routes hash
	newRoutesHash := r.calculateRoutesHash(newRoutes)
	oldRoutesHash := state.RoutesHash.ValueString()

	// Ensure gateway responses have CORS headers configured (idempotent)
	// This ensures CORS works for auth failures (401/403) even on existing gateways
	utils.Info(ctx, "Ensuring gateway responses have CORS headers...")
	if err := r.apiGatewayOps.ConfigureGatewayResponses(ctx, apiId, config); err != nil {
		utils.Warn(ctx, "Failed to configure gateway responses", map[string]interface{}{
			"api_gateway_name": apiGatewayName,
			"error":            err.Error(),
		})
		// Continue anyway - gateway responses are important but not critical
	}

	// Check if routes changed
	if newRoutesHash != oldRoutesHash {
		utils.Info(ctx, "Routes changed, updating API Gateway resources...")

		// Update API Gateway resources (this handles add/remove/update of routes)
		if err := r.apiGatewayOps.UpdateApiGatewayResources(ctx, apiId, newRoutes, config, lambdaFunctions); err != nil {
			resp.Diagnostics.AddError(
				"Failed to update API Gateway resources",
				"Could not update API Gateway resources: "+err.Error(),
			)
			return
		}

		// Add Lambda permissions for any new Lambda functions
		if err := r.addLambdaPermissions(ctx, apiId, newRoutes, lambdaFunctions, config); err != nil {
			utils.Warn(ctx, "Failed to add some Lambda permissions", map[string]interface{}{
				"error": err.Error(),
			})
		}

		// Create new deployment
		utils.Info(ctx, "Creating new deployment...")
		logGroupArn := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:/aws/apigateway/%s",
			config.AwsRegion, config.AwsAccountId, apiGatewayName)

		invokeUrl, err := r.apiGatewayOps.UpdateDeploymentAndStage(ctx, apiId, config, gatewayName, logGroupArn, apiGatewayName)
		if err != nil {
			resp.Diagnostics.AddError(
				"Failed to create deployment",
				"Could not create new API Gateway deployment: "+err.Error(),
			)
			return
		}

		plan.InvokeUrl = types.StringValue(invokeUrl)
		utils.Info(ctx, "Successfully updated API Gateway routes and created new deployment")
	} else {
		utils.Info(ctx, "No route changes detected, skipping API Gateway update")
		plan.InvokeUrl = state.InvokeUrl
	}

	// Update computed values
	plan.ID = state.ID
	plan.ApiId = state.ApiId
	plan.RoutesHash = types.StringValue(newRoutesHash)

	utils.Info(ctx, "Successfully updated Gateway resource", map[string]interface{}{
		"api_id":           apiId,
		"api_gateway_name": apiGatewayName,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}


// Delete deletes the resource and removes the Terraform state.
func (r *gatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build configuration from model
	config, err := r.buildConfigFromModel(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to build configuration",
			"Could not build configuration from resource attributes: "+err.Error(),
		)
		return
	}

	// Initialize AWS clients and managers
	if err := r.initializeManagers(ctx, config); err != nil {
		resp.Diagnostics.AddError(
			"Failed to initialize AWS clients",
			"Could not initialize AWS clients: "+err.Error(),
		)
		return
	}

	apiId := state.ApiId.ValueString()
	gatewayName := state.Name.ValueString()
	apiGatewayName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, gatewayName)

	utils.Info(ctx, "Deleting Gateway resource", map[string]interface{}{
		"api_id":           apiId,
		"api_gateway_name": apiGatewayName,
	})

	// Step 1: Delete REST API (this cascades to delete all resources, methods, integrations, deployments)
	utils.Info(ctx, "Deleting REST API...")
	if err := r.apiGatewayOps.DeleteApiGateway(ctx, apiId, apiGatewayName); err != nil {
		if !strings.Contains(err.Error(), "NotFoundException") {
			resp.Diagnostics.AddError(
				"Failed to delete REST API",
				"Could not delete API Gateway REST API: "+err.Error(),
			)
			return
		}
		utils.Info(ctx, "REST API does not exist, skipping deletion")
	}

	// Step 2: Delete CloudWatch log group
	utils.Info(ctx, "Deleting CloudWatch log group...")
	if err := r.cloudWatchManager.DeleteApiGatewayLogGroup(ctx, apiGatewayName); err != nil {
		utils.Warn(ctx, "Failed to delete CloudWatch log group", map[string]interface{}{
			"api_gateway_name": apiGatewayName,
			"error":            err.Error(),
		})
		// Continue - log group deletion is not critical
	}

	utils.Info(ctx, "Successfully deleted Gateway resource", map[string]interface{}{
		"api_id":           apiId,
		"api_gateway_name": apiGatewayName,
	})
}

// ImportState imports an existing API Gateway into Terraform state.
func (r *gatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The import ID should be the API Gateway ID
	apiId := req.ID

	utils.Info(ctx, "Importing Gateway resource", map[string]interface{}{
		"api_id": apiId,
	})

	// Set the ID attribute for the Read method to use
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), apiId)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_id"), apiId)...)

	// Note: The user will need to provide the following attributes after import:
	// - name
	// - app_name
	// - routes
	// - frontend_urls
	// These cannot be determined from AWS alone
}
