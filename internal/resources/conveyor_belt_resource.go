// internal/resources/dispatcher_resource.go
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"terraform-provider-conveyor-belt/internal/utils"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                   = &dispatcherResource{}
	_ resource.ResourceWithConfigure      = &dispatcherResource{}
	_ resource.ResourceWithImportState    = &dispatcherResource{}
	_ resource.ResourceWithModifyPlan     = &dispatcherResource{}
)

// NewDispatcherResource is a helper function to simplify the provider implementation.
func NewDispatcherResource() resource.Resource {
	return &dispatcherResource{}
}

// dispatcherResource is the resource implementation for the dispatcher orchestrator.
// It manages multiple Lambda functions and API Gateways based on a routes.tf.rb file.
type dispatcherResource struct {
	providerConfig         *DispatcherConfig
	client                 *DispatcherClient
	clients                *ResourceClients
	iamManager             *IAMManager
	cloudWatchManager      *CloudWatchManager
	basePathMappingManager *BasePathMappingManager
}

// DispatcherResourceModel describes the resource data model.
type DispatcherResourceModel struct {
	ID types.String `tfsdk:"id"`

	// Required inputs
	Source          types.String `tfsdk:"source"`
	AppName         types.String `tfsdk:"app_name"`
	LambdaSourceDir types.String `tfsdk:"lambda_source_dir"`
	FrontendUrls    types.List   `tfsdk:"frontend_urls"`

	// Optional inputs
	CognitoUserPoolArns  types.List   `tfsdk:"cognito_user_pool_arns"`
	SharedIamPolicyArns  types.List   `tfsdk:"shared_iam_policy_arns"`
	LambdaLayerArns      types.List   `tfsdk:"lambda_layer_arns"`
	LambdaSharedDirs     types.List   `tfsdk:"lambda_shared_dirs"`
	LambdaGemDirs        types.List   `tfsdk:"lambda_gem_dirs"`
	ReadOnlyTables       types.List   `tfsdk:"read_only_tables"`
	ReadWriteTables      types.List   `tfsdk:"read_write_tables"`
	CustomDomainName     types.String `tfsdk:"custom_domain_name"`
	FriendlyErrors       types.Bool   `tfsdk:"friendly_errors"`
	SchemaSource         types.String `tfsdk:"schema_source"`
	SuppressTableEnvVars types.Bool   `tfsdk:"suppress_table_env_vars"`

	// Lambda configuration overrides
	LambdaConfig types.Dynamic `tfsdk:"lambda_config"`

	// Alarm configuration
	AlarmConfig types.Object `tfsdk:"alarm_config"`

	// Computed outputs (plan-time known)
	GatewayNames types.List `tfsdk:"gateway_names"`
	LambdaNames  types.List `tfsdk:"lambda_names"`

	// Computed outputs
	LambdaFunctions    types.Map    `tfsdk:"lambda_functions"`
	ApiGatewayIds      types.Map    `tfsdk:"api_gateway_ids"`
	ApiGatewayUrls     types.Map    `tfsdk:"api_gateway_urls"`
	RoutesHash         types.String `tfsdk:"routes_hash"`
	LambdaHashes       types.Map    `tfsdk:"lambda_hashes"`
	LambdaSourceHashes types.Map    `tfsdk:"lambda_source_hashes"`
	LambdaConfigHashes types.Map    `tfsdk:"lambda_config_hashes"`
	GatewayHashes      types.Map    `tfsdk:"gateway_hashes"`
	RoutesJson         types.String `tfsdk:"routes_json"`
	BasePathMappings   types.Map    `tfsdk:"base_path_mappings"`
	CustomDomainUrl    types.String `tfsdk:"custom_domain_url"`
	ModelHashes        types.Map    `tfsdk:"model_hashes"`
	OpenAPISpecHashes  types.Map    `tfsdk:"openapi_spec_hashes"`
}

// Metadata returns the resource type name.
func (r *dispatcherResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName
}

// Schema defines the schema for the resource.
func (r *dispatcherResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Orchestrates AWS serverless infrastructure from a Ruby routes DSL file. " +
			"Automatically creates Lambda functions and API Gateways based on the routes defined in the source file.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Resource identifier",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"source": schema.StringAttribute{
				Description: "Path to routes.tf.rb file that defines API routes",
				Required:    true,
			},
			"app_name": schema.StringAttribute{
				Description: "Application name for resource naming (e.g., 'myapp')",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"lambda_source_dir": schema.StringAttribute{
				Description: "Directory containing Lambda source files (.rb files)",
				Required:    true,
			},
			"frontend_urls": schema.ListAttribute{
				Description: "Frontend URLs for CORS configuration",
				Required:    true,
				ElementType: types.StringType,
			},
			"cognito_user_pool_arns": schema.ListAttribute{
				Description: "Cognito User Pool ARNs for API Gateway authorizers",
				Optional:    true,
				ElementType: types.StringType,
			},
			"shared_iam_policy_arns": schema.ListAttribute{
				Description: "IAM policy ARNs to attach to all Lambda execution roles",
				Optional:    true,
				ElementType: types.StringType,
			},
			"lambda_layer_arns": schema.ListAttribute{
				Description: "Lambda Layer ARNs to attach to all Lambda functions",
				Optional:    true,
				ElementType: types.StringType,
			},
			"lambda_shared_dirs": schema.ListAttribute{
				Description: "Shared directories to include in Lambda packages (default: models, lib, helpers, templates)",
				Optional:    true,
				ElementType: types.StringType,
			},
			"lambda_gem_dirs": schema.ListAttribute{
				Description: "Directories (relative to lambda_source_dir) to include in the Docker gem build context for path-based gems. " +
					"Use this when your Gemfile references gems via `path:` that live alongside your Lambda source.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"read_only_tables": schema.ListAttribute{
				Description: "DynamoDB tables that all Lambdas get read-only access to",
				Optional:    true,
				ElementType: types.StringType,
			},
			"read_write_tables": schema.ListAttribute{
				Description: "DynamoDB tables that all Lambdas get read-write access to",
				Optional:    true,
				ElementType: types.StringType,
			},
			"custom_domain_name": schema.StringAttribute{
				Description: "Custom domain name for unified API access (e.g., 'api.example.com'). " +
					"When provided, Dispatcher creates base path mappings for each gateway. " +
					"The custom domain must be created separately in your Terraform configuration.",
				Optional: true,
			},
			"friendly_errors": schema.BoolAttribute{
				Description: "Enable friendly error messages for missing routes in non-production environments. " +
					"When true, API Gateway returns detailed debugging information for 404 errors (route not found, wrong HTTP method). " +
					"Recommended: set to true for dev/uat/staging, false for production. Default: false.",
				Optional: true,
			},
			"suppress_table_env_vars": schema.BoolAttribute{
				Description: "When true, do not generate individual *_TABLE_NAME environment variables or the TABLES " +
					"comma-separated list on Lambda functions. IAM policies from tables: arrays are still created. " +
					"Use this when your Lambda code derives table names from APP_NAME and ENVIRONMENT instead. Default: false.",
				Optional: true,
			},
			"schema_source": schema.StringAttribute{
				Description: "Path to schema.tf.rb file defining API Gateway request/response models. " +
					"When provided, Dispatcher creates API Gateway Models and Request Validators " +
					"for runtime payload validation at the gateway level.",
				Optional: true,
			},
			"lambda_config": schema.DynamicAttribute{
				Description: "Per-lambda Lambda configuration overrides. Keys are lambda names (or 'shared' for all). " +
					"Values can include: env_vars, timeout, memory_size, dynamodb_tables, s3_buckets, ses_emails, sns_triggers, sqs_triggers",
				Optional: true,
			},
			"alarm_config": schema.SingleNestedAttribute{
				Description: "CloudWatch alarm configuration for Lambda functions",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Description: "Enable CloudWatch alarms",
						Optional:    true,
					},
					"sns_topic_arn": schema.StringAttribute{
						Description: "SNS topic ARN for alarm notifications",
						Optional:    true,
					},
					"error_enabled": schema.BoolAttribute{
						Description: "Enable error alarms",
						Optional:    true,
					},
					"error_threshold": schema.Int64Attribute{
						Description: "Error count threshold",
						Optional:    true,
					},
					"error_period": schema.Int64Attribute{
						Description: "Error alarm period in seconds",
						Optional:    true,
					},
					"error_evaluation_periods": schema.Int64Attribute{
						Description: "Number of periods to evaluate",
						Optional:    true,
					},
					"duration_enabled": schema.BoolAttribute{
						Description: "Enable duration alarms",
						Optional:    true,
					},
					"duration_threshold": schema.Int64Attribute{
						Description: "Duration threshold in milliseconds",
						Optional:    true,
					},
					"duration_period": schema.Int64Attribute{
						Description: "Duration alarm period in seconds",
						Optional:    true,
					},
					"duration_evaluation_periods": schema.Int64Attribute{
						Description: "Number of periods to evaluate",
						Optional:    true,
					},
					"throttle_enabled": schema.BoolAttribute{
						Description: "Enable throttle alarms",
						Optional:    true,
					},
					"throttle_threshold": schema.Int64Attribute{
						Description: "Throttle count threshold",
						Optional:    true,
					},
					"throttle_period": schema.Int64Attribute{
						Description: "Throttle alarm period in seconds",
						Optional:    true,
					},
					"throttle_evaluation_periods": schema.Int64Attribute{
						Description: "Number of periods to evaluate",
						Optional:    true,
					},
					"lambda_overrides": schema.DynamicAttribute{
						Description: "Per-lambda alarm overrides",
						Optional:    true,
					},
				},
			},
			// Computed outputs
			"lambda_functions": schema.MapAttribute{
				Description: "Map of lambda name to Lambda ARN",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"gateway_names": schema.ListAttribute{
				Description: "List of gateway namespace names derived from routes (plan-time known, safe for use in for_each)",
				Computed:    true,
				ElementType: types.StringType,
			},
			"lambda_names": schema.ListAttribute{
				Description: "List of lambda function names derived from routes and source directory (plan-time known, safe for use in for_each)",
				Computed:    true,
				ElementType: types.StringType,
			},
			"api_gateway_ids": schema.MapAttribute{
				Description: "Map of gateway name to API Gateway ID",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"api_gateway_urls": schema.MapAttribute{
				Description: "Map of gateway name to API Gateway invoke URL",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"routes_hash": schema.StringAttribute{
				Description: "Hash of parsed routes for change detection",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"lambda_hashes": schema.MapAttribute{
				Description: "Map of lambda name to source+config hash",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"lambda_source_hashes": schema.MapAttribute{
				Description: "Map of lambda name to source-only hash for precise change detection",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"lambda_config_hashes": schema.MapAttribute{
				Description: "Map of lambda name to config-only hash for precise change detection",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"gateway_hashes": schema.MapAttribute{
				Description: "Map of gateway name to routes hash",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"routes_json": schema.StringAttribute{
				Description: "JSON representation of parsed routes for change detection (internal use)",
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"base_path_mappings": schema.MapAttribute{
				Description: "Map of gateway names to their base path mappings (e.g., {'ops': '/ops'})",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"custom_domain_url": schema.StringAttribute{
				Description: "Base URL for the custom domain (e.g., 'https://api.example.com')",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"model_hashes": schema.MapAttribute{
				Description: "Map of gateway name to model definition hash for change detection",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"openapi_spec_hashes": schema.MapAttribute{
				Description: "Map of gateway name to OpenAPI spec hash for change detection (Phase 1: read-only, generated as side effect)",
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *dispatcherResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

	// Store the client for cleanup
	r.client = client

	// Store the provider configuration including defaults
	r.providerConfig = &DispatcherConfig{
		Environment:            client.Environment,
		AwsRegion:              client.AwsRegion,
		DefaultLambdaTimeout:   client.DefaultLambdaTimeout,
		DefaultLambdaMemory:    client.DefaultLambdaMemory,
		DefaultTags:            client.DefaultTags,
		DockerBuildConcurrency: client.DockerBuildConcurrency,
	}
}

// ModifyPlan is called during the plan phase to detect changes in the routes file
// and mark computed attributes as unknown if they will change during apply.
// This is critical because the `source` attribute is just a file path - Terraform
// doesn't know the file contents changed unless we tell it.
func (r *dispatcherResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Use tflog for Terraform-native logging that shows up with TF_LOG
	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] ModifyPlan called")

	// Skip if this is a create or destroy operation
	if req.State.Raw.IsNull() {
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] State is null (create operation) - setting plan-time known attributes")

		// Even on create, we can parse routes to set gateway_names and lambda_names
		// so they are known at plan time for for_each usage.
		if r.providerConfig != nil {
			var createPlan DispatcherResourceModel
			resp.Diagnostics.Append(req.Plan.Get(ctx, &createPlan)...)
			if !resp.Diagnostics.HasError() && !createPlan.Source.IsUnknown() {
				schemaSource := ""
				if !createPlan.SchemaSource.IsNull() && !createPlan.SchemaSource.IsUnknown() {
					schemaSource = createPlan.SchemaSource.ValueString()
				}
				routes, _, err := r.parseRoutesAndModels(ctx, createPlan.Source.ValueString(), schemaSource)
				if err == nil {
					lambdaConfig, _ := r.extractLambdaConfig(ctx, &createPlan)
					lambdaSourceDir := ""
					if !createPlan.LambdaSourceDir.IsUnknown() {
						lambdaSourceDir = createPlan.LambdaSourceDir.ValueString()
					}
					lambdas, gateways := r.extractResourcesWithSourceDir(routes, lambdaConfig, lambdaSourceDir)

					gatewayNamesList, d := types.ListValueFrom(ctx, types.StringType, gateways)
					resp.Diagnostics.Append(d...)
					createPlan.GatewayNames = gatewayNamesList

					lambdaNamesList, d := types.ListValueFrom(ctx, types.StringType, lambdas)
					resp.Diagnostics.Append(d...)
					createPlan.LambdaNames = lambdaNamesList

					resp.Diagnostics.Append(resp.Plan.Set(ctx, &createPlan)...)
				}
			}
		}
		return
	}
	if req.Plan.Raw.IsNull() {
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Skipping - plan is null (destroy operation)")
		return
	}

	// Skip if provider config is not yet available
	if r.providerConfig == nil {
		tflog.Warn(ctx, "[CONVEYOR-BELT_PLAN] Skipping - provider config is nil (Configure not called yet?)")
		return
	}

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Starting change detection", map[string]interface{}{})

	var plan, state DispatcherResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		tflog.Error(ctx, "[CONVEYOR-BELT_PLAN] Failed to get plan or state")
		return
	}

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Parsing routes from source file", map[string]interface{}{
		"source": plan.Source.ValueString(),
	})

	// Parse routes from the source file to detect changes
	schemaSource := ""
	if !plan.SchemaSource.IsNull() && !plan.SchemaSource.IsUnknown() {
		schemaSource = plan.SchemaSource.ValueString()
	}
	routes, models, err := r.parseRoutesAndModels(ctx, plan.Source.ValueString(), schemaSource)
	if err != nil {
		tflog.Error(ctx, "[CONVEYOR-BELT_PLAN] Failed to parse routes", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Parsed routes successfully", map[string]interface{}{
		"route_count": len(routes),
	})

	// Extract lambda_config from plan
	lambdaConfig, err := r.extractLambdaConfig(ctx, &plan)
	if err != nil {
		tflog.Error(ctx, "[CONVEYOR-BELT_PLAN] Failed to extract lambda_config", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Extract tables from plan for hash calculation
	var readOnlyTables, readWriteTables []string
	if !plan.ReadOnlyTables.IsNull() && !plan.ReadOnlyTables.IsUnknown() {
		plan.ReadOnlyTables.ElementsAs(ctx, &readOnlyTables, false)
	}
	if !plan.ReadWriteTables.IsNull() && !plan.ReadWriteTables.IsUnknown() {
		plan.ReadWriteTables.ElementsAs(ctx, &readWriteTables, false)
	}

	// Calculate new routes hash
	newRoutesHash, err := calculateConfigHash(routes, models, lambdaConfig, readOnlyTables, readWriteTables)
	if err != nil {
		tflog.Error(ctx, "[CONVEYOR-BELT_PLAN] Failed to calculate routes hash", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Get old routes hash from state
	oldRoutesHash := state.RoutesHash.ValueString()

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Comparing routes hashes", map[string]interface{}{
		"old_hash": oldRoutesHash,
		"new_hash": newRoutesHash,
		"changed":  newRoutesHash != oldRoutesHash,
	})

	// Extract resources to check for additions/removals
	lambdaSourceDir := plan.LambdaSourceDir.ValueString()
	lambdas, gateways := r.extractResourcesWithSourceDir(routes, lambdaConfig, lambdaSourceDir)

	// Always set gateway_names and lambda_names as plan-time known values.
	// These are derived purely from the source file and require no AWS API calls.
	gatewayNamesList, d := types.ListValueFrom(ctx, types.StringType, gateways)
	resp.Diagnostics.Append(d...)
	plan.GatewayNames = gatewayNamesList

	lambdaNamesList, d := types.ListValueFrom(ctx, types.StringType, lambdas)
	resp.Diagnostics.Append(d...)
	plan.LambdaNames = lambdaNamesList

	// Get old lambda hashes from state
	var oldLambdaHashes map[string]string
	if !state.LambdaHashes.IsNull() && !state.LambdaHashes.IsUnknown() {
		oldLambdaHashes = make(map[string]string)
		state.LambdaHashes.ElementsAs(ctx, &oldLambdaHashes, false)
	}

	// Get old gateway hashes from state
	var oldGatewayHashes map[string]string
	if !state.GatewayHashes.IsNull() && !state.GatewayHashes.IsUnknown() {
		oldGatewayHashes = make(map[string]string)
		state.GatewayHashes.ElementsAs(ctx, &oldGatewayHashes, false)
	}

	// Post-import detection: if hashes and lambda_functions are both empty,
	// this is likely the first plan after import. Discover existing resources
	// from AWS so we can correctly classify them as UPDATE rather than CREATE.
	var oldLambdaARNs map[string]string
	if !state.LambdaFunctions.IsNull() && !state.LambdaFunctions.IsUnknown() {
		oldLambdaARNs = make(map[string]string)
		state.LambdaFunctions.ElementsAs(ctx, &oldLambdaARNs, false)
	}
	var oldGatewayIDs map[string]string
	if !state.ApiGatewayIds.IsNull() && !state.ApiGatewayIds.IsUnknown() {
		oldGatewayIDs = make(map[string]string)
		state.ApiGatewayIds.ElementsAs(ctx, &oldGatewayIDs, false)
	}

	postImportState := false
	if len(oldLambdaHashes) == 0 && len(oldLambdaARNs) == 0 && r.providerConfig != nil {
		// Initialize AWS clients for post-import discovery
		planConfig := &DispatcherConfig{
			Environment: r.providerConfig.Environment,
			AwsRegion:   r.providerConfig.AwsRegion,
			AppName:     plan.AppName.ValueString(),
		}
		if err := r.initializeManagers(ctx, planConfig); err == nil {
			tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Post-import state detected, discovering existing resources from AWS")
			prefix := fmt.Sprintf("%s-%s-", plan.AppName.ValueString(), r.providerConfig.Environment)
			oldLambdaARNs, oldGatewayIDs = r.discoverManagedResources(ctx, prefix, lambdas, gateways)
		} else {
			tflog.Warn(ctx, "[CONVEYOR-BELT_PLAN] Failed to initialize AWS clients for import discovery", map[string]interface{}{
				"error": err.Error(),
			})
		}
		// After import, hash inputs may not be stable (no prior state to compare against).
		// Mark hashes as unknown so Terraform defers consistency checks until apply.
		postImportState = true
	}

	// Calculate new gateway hashes to determine which gateways will be updated
	var planFrontendUrls []string
	if !plan.FrontendUrls.IsNull() && !plan.FrontendUrls.IsUnknown() {
		plan.FrontendUrls.ElementsAs(ctx, &planFrontendUrls, false)
	}
	newGatewayHashes, _ := calculateAllGatewayHashes(routes, planFrontendUrls)

	// Check for new or removed lambdas
	newLambdaSet := make(map[string]bool)
	for _, l := range lambdas {
		newLambdaSet[l] = true
	}
	var lambdasAdded, lambdasRemoved []string
	for _, newLambda := range lambdas {
		// A lambda is "new" only if it has no hash in state AND no ARN in lambda_functions.
		// After import, hashes are empty but lambda_functions is populated by discovery.
		_, hasHash := oldLambdaHashes[newLambda]
		if !hasHash {
			lambdasAdded = append(lambdasAdded, newLambda)
		}
	}

	// If a lambda exists in lambda_functions (discovered from AWS), don't treat it as new.
	// This handles the post-import case where hashes are empty but resources exist.
	if len(oldLambdaARNs) > 0 {
		filtered := lambdasAdded[:0]
		for _, l := range lambdasAdded {
			if _, hasARN := oldLambdaARNs[l]; !hasARN {
				filtered = append(filtered, l)
			}
		}
		lambdasAdded = filtered
	}

	lambdaRemovedSet := make(map[string]bool)
	for oldLambda := range oldLambdaHashes {
		if !newLambdaSet[oldLambda] {
			lambdasRemoved = append(lambdasRemoved, oldLambda)
			lambdaRemovedSet[oldLambda] = true
		}
	}
	// Also check lambda_functions for lambdas not caught by hash comparison
	for oldLambda := range oldLambdaARNs {
		if !newLambdaSet[oldLambda] && !lambdaRemovedSet[oldLambda] {
			lambdasRemoved = append(lambdasRemoved, oldLambda)
		}
	}

	// Check for new or removed gateways
	newGatewaySet := make(map[string]bool)
	for _, g := range gateways {
		newGatewaySet[g] = true
	}
	var gatewaysAdded, gatewaysRemoved []string
	for _, newGateway := range gateways {
		if _, exists := oldGatewayHashes[newGateway]; !exists {
			gatewaysAdded = append(gatewaysAdded, newGateway)
		}
	}

	// If a gateway exists in api_gateway_ids (discovered from AWS), don't treat it as new.
	// This handles the post-import case where hashes are empty but resources exist.
	if len(oldGatewayIDs) > 0 {
		filtered := gatewaysAdded[:0]
		for _, g := range gatewaysAdded {
			if _, hasID := oldGatewayIDs[g]; !hasID {
				filtered = append(filtered, g)
			}
		}
		gatewaysAdded = filtered
	}

	removedSet := make(map[string]bool)
	for oldGateway := range oldGatewayHashes {
		if !newGatewaySet[oldGateway] {
			gatewaysRemoved = append(gatewaysRemoved, oldGateway)
			removedSet[oldGateway] = true
		}
	}
	// Also check api_gateway_ids for gateways not caught by hash comparison
	for oldGateway := range oldGatewayIDs {
		if !newGatewaySet[oldGateway] && !removedSet[oldGateway] {
			gatewaysRemoved = append(gatewaysRemoved, oldGateway)
		}
	}

	// Check which gateways have hash changes (will be updated)
	var gatewaysToUpdate []string
	for _, g := range gateways {
		if oldHash, exists := oldGatewayHashes[g]; exists {
			if newHash, ok := newGatewayHashes[g]; ok && oldHash != newHash {
				gatewaysToUpdate = append(gatewaysToUpdate, g)
				tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Gateway hash changed - will update", map[string]interface{}{
					"gateway":  g,
					"old_hash": oldHash,
					"new_hash": newHash,
				})
			}
		}
	}

	// Calculate route changes for each gateway that will be updated
	var routeChangeSummary []string
	for _, g := range gatewaysToUpdate {
		oldGatewayRoutes := filterRoutesByGatewayFromState(ctx, &state, g)
		newGatewayRoutes := filterRoutesByGateway(routes, g)
		
		added, removed := diffRoutes(oldGatewayRoutes, newGatewayRoutes)
		if len(added) > 0 || len(removed) > 0 {
			// Gateway header with counts
			var counts []string
			if len(added) > 0 {
				counts = append(counts, fmt.Sprintf("+%d routes", len(added)))
			}
			if len(removed) > 0 {
				counts = append(counts, fmt.Sprintf("-%d routes", len(removed)))
			}
			routeChangeSummary = append(routeChangeSummary, fmt.Sprintf("Gateway '%s': %s", g, strings.Join(counts, ", ")))
			
			// Each added route on its own line
			for _, r := range added {
				routeChangeSummary = append(routeChangeSummary, fmt.Sprintf("  + %s %s", r.Verb, r.Path))
			}
			// Each removed route on its own line
			for _, r := range removed {
				routeChangeSummary = append(routeChangeSummary, fmt.Sprintf("  - %s %s", r.Verb, r.Path))
			}
		}
	}

	// Determine what needs to be marked as unknown
	routesChanged := newRoutesHash != oldRoutesHash
	lambdaStructureChanged := len(lambdasAdded) > 0 || len(lambdasRemoved) > 0
	gatewayStructureChanged := len(gatewaysAdded) > 0 || len(gatewaysRemoved) > 0

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Change summary", map[string]interface{}{
		"routes_changed":            routesChanged,
		"lambdas_added":             lambdasAdded,
		"lambdas_removed":           lambdasRemoved,
		"gateways_added":            gatewaysAdded,
		"gateways_removed":          gatewaysRemoved,
		"gateways_to_update":        gatewaysToUpdate,
		"lambda_structure_changed":  lambdaStructureChanged,
		"gateway_structure_changed": gatewayStructureChanged,
		"route_changes":             routeChangeSummary,
	})

	// Add a warning to show the user what will change
	if routesChanged || lambdaStructureChanged || gatewayStructureChanged || len(gatewaysToUpdate) > 0 {
		var changeMessages []string
		
		if len(lambdasAdded) > 0 {
			changeMessages = append(changeMessages, fmt.Sprintf("Lambdas to CREATE: [%s]", strings.Join(lambdasAdded, ", ")))
		}
		if len(lambdasRemoved) > 0 {
			changeMessages = append(changeMessages, fmt.Sprintf("Lambdas to DELETE: [%s]", strings.Join(lambdasRemoved, ", ")))
		}
		if len(gatewaysAdded) > 0 {
			changeMessages = append(changeMessages, fmt.Sprintf("Gateways to CREATE: [%s]", strings.Join(gatewaysAdded, ", ")))
		}
		if len(gatewaysRemoved) > 0 {
			changeMessages = append(changeMessages, fmt.Sprintf("Gateways to DELETE: [%s]", strings.Join(gatewaysRemoved, ", ")))
		}
		if len(gatewaysToUpdate) > 0 {
			changeMessages = append(changeMessages, fmt.Sprintf("Gateways to UPDATE: [%s]", strings.Join(gatewaysToUpdate, ", ")))
		}
		for _, summary := range routeChangeSummary {
			changeMessages = append(changeMessages, summary)
		}
		
		if len(changeMessages) > 0 {
			resp.Diagnostics.AddWarning(
				"Dispatcher Resource Changes Detected",
				strings.Join(changeMessages, "\n"),
			)
		}
	}

	// --- Compute all hash inputs needed for both branches ---

	// Get shared dirs for hash calculation
	var sharedDirs []string
	if !plan.LambdaSharedDirs.IsNull() && !plan.LambdaSharedDirs.IsUnknown() {
		plan.LambdaSharedDirs.ElementsAs(ctx, &sharedDirs, false)
	}
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	// Get gem dirs for hash calculation
	var gemDirs []string
	if !plan.LambdaGemDirs.IsNull() && !plan.LambdaGemDirs.IsUnknown() {
		plan.LambdaGemDirs.ElementsAs(ctx, &gemDirs, false)
	}

	// Get layer ARNs for hash calculation
	var layerArns []string
	hashInputsUnknown := postImportState
	if plan.LambdaLayerArns.IsUnknown() {
		hashInputsUnknown = true
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] LambdaLayerArns is unknown — hash inputs incomplete")
	} else if !plan.LambdaLayerArns.IsNull() {
		plan.LambdaLayerArns.ElementsAs(ctx, &layerArns, false)
	}

	// Get shared IAM policy ARNs for hash calculation
	var sharedIamPolicyArns []string
	if plan.SharedIamPolicyArns.IsUnknown() {
		hashInputsUnknown = true
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] SharedIamPolicyArns is unknown — hash inputs incomplete")
	} else if !plan.SharedIamPolicyArns.IsNull() {
		plan.SharedIamPolicyArns.ElementsAs(ctx, &sharedIamPolicyArns, false)
	}

	// Extract alarm config for hash calculation (must match what Update uses)
	var alarmConfig *AlarmConfig
	if plan.AlarmConfig.IsUnknown() {
		hashInputsUnknown = true
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] AlarmConfig is unknown — hash inputs incomplete")
	} else if !plan.AlarmConfig.IsNull() {
		alarmConfig = r.extractAlarmConfig(ctx, &plan)
	}

	// When any hash input is unknown (e.g., shared_iam_policy_arns references a resource
	// being created), we cannot compute accurate hashes. Mark all dependent hash outputs
	// as unknown so Terraform defers consistency checks until apply.
	if hashInputsUnknown {
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Hash inputs unknown — marking lambda hashes as unknown to avoid inconsistent plan")
		plan.LambdaHashes = types.MapUnknown(types.StringType)
		plan.LambdaConfigHashes = types.MapUnknown(types.StringType)
		plan.RoutesHash = types.StringUnknown()
		plan.RoutesJson = types.StringUnknown()

		// Source hashes don't depend on IAM/layer/alarm config, so compute them if possible
		newSrcHashes, srcErr := calculateAllLambdaSourceHashes(lambdas, lambdaSourceDir, sharedDirs, gemDirs...)
		if srcErr != nil {
			plan.LambdaSourceHashes = types.MapUnknown(types.StringType)
		} else {
			shMap, d := types.MapValueFrom(ctx, types.StringType, newSrcHashes)
			resp.Diagnostics.Append(d...)
			plan.LambdaSourceHashes = shMap
		}

		// Gateway and model hashes don't depend on these inputs either
		newGatewayHashesMap, gwErr := types.MapValueFrom(ctx, types.StringType, newGatewayHashes)
		if gwErr.HasError() {
			resp.Diagnostics.Append(gwErr...)
		} else {
			plan.GatewayHashes = newGatewayHashesMap
		}

		if lambdaStructureChanged {
			plan.LambdaFunctions = types.MapUnknown(types.StringType)
		}
		if gatewayStructureChanged {
			plan.ApiGatewayIds = types.MapUnknown(types.StringType)
			plan.ApiGatewayUrls = types.MapUnknown(types.StringType)
			plan.BasePathMappings = types.MapUnknown(types.StringType)
		}

		// Post-import: all computed outputs will be populated during apply.
		// Mark them as unknown so Terraform doesn't reject new values.
		if postImportState {
			plan.LambdaFunctions = types.MapUnknown(types.StringType)
			plan.ApiGatewayIds = types.MapUnknown(types.StringType)
			plan.ApiGatewayUrls = types.MapUnknown(types.StringType)
			plan.BasePathMappings = types.MapUnknown(types.StringType)
			plan.CustomDomainUrl = types.StringUnknown()
			plan.LambdaSourceHashes = types.MapUnknown(types.StringType)
			plan.GatewayHashes = types.MapUnknown(types.StringType)
			plan.ModelHashes = types.MapUnknown(types.StringType)
			plan.OpenAPISpecHashes = types.MapUnknown(types.StringType)
		}

		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}

	// If routes hash changed, compute new hashes surgically — only mark maps as unknown
	// when their entries actually changed, so `terraform plan` shows precise diffs.
	if routesChanged || lambdaStructureChanged || gatewayStructureChanged {
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Changes detected - computing precise hash updates")

		needsPlanSet := false

		// Always set the new routes hash (it changed by definition)
		plan.RoutesHash = types.StringValue(newRoutesHash)
		plan.RoutesJson = types.StringUnknown() // JSON is always recomputed in Update
		needsPlanSet = true

		// Compute and set gateway hashes — only changed gateways get new values
		newGatewayHashesMap, gwErr := types.MapValueFrom(ctx, types.StringType, newGatewayHashes)
		if gwErr.HasError() {
			resp.Diagnostics.Append(gwErr...)
		} else {
			plan.GatewayHashes = newGatewayHashesMap
		}

		// Compute and set lambda hashes
		newAllLambdaHashes, err := calculateAllLambdaHashes(lambdas, routes, lambdaConfig, lambdaSourceDir, sharedDirs, layerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
		if err != nil {
			tflog.Warn(ctx, "[CONVEYOR-BELT_PLAN] Failed to calculate lambda hashes, marking unknown", map[string]interface{}{"error": err.Error()})
			plan.LambdaHashes = types.MapUnknown(types.StringType)
			plan.LambdaConfigHashes = types.MapUnknown(types.StringType)
		} else {
			lhMap, d := types.MapValueFrom(ctx, types.StringType, newAllLambdaHashes)
			resp.Diagnostics.Append(d...)
			plan.LambdaHashes = lhMap

			// Config hashes
			newCfgHashes, cfgErr := calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, layerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
			if cfgErr != nil {
				plan.LambdaConfigHashes = types.MapUnknown(types.StringType)
			} else {
				chMap, d := types.MapValueFrom(ctx, types.StringType, newCfgHashes)
				resp.Diagnostics.Append(d...)
				plan.LambdaConfigHashes = chMap
			}
		}

		// Source hashes (routes don't affect source hashes, but structure changes do)
		newSrcHashes, srcErr := calculateAllLambdaSourceHashes(lambdas, lambdaSourceDir, sharedDirs, gemDirs...)
		if srcErr != nil {
			plan.LambdaSourceHashes = types.MapUnknown(types.StringType)
		} else {
			shMap, d := types.MapValueFrom(ctx, types.StringType, newSrcHashes)
			resp.Diagnostics.Append(d...)
			plan.LambdaSourceHashes = shMap
		}

		// Model hashes — compute per-gateway
		newModelHashes := make(map[string]string)
		for _, gw := range gateways {
			gwModels := GetModelsForGateway(routes, models, gw)
			if h, err := CalculateModelHash(gwModels); err == nil && h != "" {
				newModelHashes[gw] = h
			}
		}
		if len(newModelHashes) > 0 {
			mhMap, d := types.MapValueFrom(ctx, types.StringType, newModelHashes)
			resp.Diagnostics.Append(d...)
			plan.ModelHashes = mhMap
		} else {
			plan.ModelHashes = types.MapNull(types.StringType)
		}

		// OpenAPI spec hashes — can't compute during Plan (need Lambda ARNs),
		// but only mark changed gateways as unknown, preserving existing hashes
		if len(gatewaysToUpdate) > 0 || gatewayStructureChanged {
			oldSpecHashes := make(map[string]string)
			if !state.OpenAPISpecHashes.IsNull() && !state.OpenAPISpecHashes.IsUnknown() {
				state.OpenAPISpecHashes.ElementsAs(ctx, &oldSpecHashes, false)
			}
			// Build map with known values for unchanged gateways, unknown for changed
			specElements := make(map[string]attr.Value)
			gatewaysToUpdateSet := make(map[string]bool)
			for _, gw := range gatewaysToUpdate {
				gatewaysToUpdateSet[gw] = true
			}
			for _, gw := range gateways {
				if gatewayStructureChanged || gatewaysToUpdateSet[gw] {
					specElements[gw] = types.StringUnknown()
				} else if h, ok := oldSpecHashes[gw]; ok {
					specElements[gw] = types.StringValue(h)
				}
			}
			plan.OpenAPISpecHashes, _ = types.MapValue(types.StringType, specElements)
		}

		// Only mark lambda_functions as unknown if lambdas were added/removed
		if lambdaStructureChanged {
			plan.LambdaFunctions = types.MapUnknown(types.StringType)
		}

		// Only mark gateway outputs as unknown if gateways were added/removed
		if gatewayStructureChanged {
			plan.ApiGatewayIds = types.MapUnknown(types.StringType)
			plan.ApiGatewayUrls = types.MapUnknown(types.StringType)
			plan.BasePathMappings = types.MapUnknown(types.StringType)
		}

		if needsPlanSet {
			resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		}
		return
	}

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Routes hash unchanged, checking lambda source hashes")

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Old lambda hashes from state", map[string]interface{}{
		"hashes": oldLambdaHashes,
	})

	// Check which lambdas have hash changes and collect them for reporting
	var lambdasToUpdate []string
	
	// Pre-filter routes by lambda for consistent hash computation
	routeLambdaGroups := utils.GroupByLambda(routes)

	for _, lambda := range lambdas {
		lambdaRoutes := routeLambdaGroups[lambda]
		newHash, err := calculateLambdaHash(lambdaRoutes, lambdaConfig, lambda, lambdaSourceDir, sharedDirs, layerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
		if err != nil {
			tflog.Warn(ctx, "[CONVEYOR-BELT_PLAN] Failed to calculate lambda hash", map[string]interface{}{
				"lambda": lambda,
				"error":  err.Error(),
			})
			continue
		}
		oldHash := oldLambdaHashes[lambda]
		tflog.Debug(ctx, "[CONVEYOR-BELT_PLAN] Lambda hash comparison", map[string]interface{}{
			"lambda":   lambda,
			"old_hash": oldHash,
			"new_hash": newHash,
			"changed":  newHash != oldHash,
		})
		if newHash != oldHash {
			tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Lambda hash CHANGED", map[string]interface{}{
				"lambda":   lambda,
				"old_hash": oldHash,
				"new_hash": newHash,
			})
			lambdasToUpdate = append(lambdasToUpdate, lambda)
		}
	}

	if len(lambdasToUpdate) > 0 {
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Lambda changes detected", map[string]interface{}{
			"lambdas_to_update": lambdasToUpdate,
		})
		
		// Report which lambdas will be updated in the plan output
		resp.Diagnostics.AddWarning(
			"Lambda Changes Detected",
			fmt.Sprintf("Lambdas to UPDATE: [%s]\n\nNote: Changes to shared directories (%s) affect all Lambdas.", 
				strings.Join(lambdasToUpdate, ", "),
				strings.Join(sharedDirs, ", ")),
		)
		
		// Compute actual new hashes instead of marking unknown
		newAllLambdaHashes, err := calculateAllLambdaHashes(lambdas, routes, lambdaConfig, lambdaSourceDir, sharedDirs, layerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
		if err != nil {
			plan.LambdaHashes = types.MapUnknown(types.StringType)
			plan.LambdaSourceHashes = types.MapUnknown(types.StringType)
			plan.LambdaConfigHashes = types.MapUnknown(types.StringType)
		} else {
			lhMap, d := types.MapValueFrom(ctx, types.StringType, newAllLambdaHashes)
			resp.Diagnostics.Append(d...)
			plan.LambdaHashes = lhMap

			newSrcHashes, _ := calculateAllLambdaSourceHashes(lambdas, lambdaSourceDir, sharedDirs, gemDirs...)
			if newSrcHashes != nil {
				shMap, d := types.MapValueFrom(ctx, types.StringType, newSrcHashes)
				resp.Diagnostics.Append(d...)
				plan.LambdaSourceHashes = shMap
			} else {
				plan.LambdaSourceHashes = types.MapUnknown(types.StringType)
			}

			newCfgHashes, _ := calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, layerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
			if newCfgHashes != nil {
				chMap, d := types.MapValueFrom(ctx, types.StringType, newCfgHashes)
				resp.Diagnostics.Append(d...)
				plan.LambdaConfigHashes = chMap
			} else {
				plan.LambdaConfigHashes = types.MapUnknown(types.StringType)
			}
		}
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	} else {
		tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] No changes detected")
		// Still need to persist plan-time known attributes (gateway_names, lambda_names)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	}

	// Check for model hash drift (Read may have cleared stale model hashes from state)
	var oldModelHashes map[string]string
	if !state.ModelHashes.IsNull() && !state.ModelHashes.IsUnknown() {
		oldModelHashes = make(map[string]string)
		state.ModelHashes.ElementsAs(ctx, &oldModelHashes, false)
	}
	newModelHashes := make(map[string]string)
	for _, gw := range gateways {
		gwModels := GetModelsForGateway(routes, models, gw)
		if h, err := CalculateModelHash(gwModels); err == nil && h != "" {
			newModelHashes[gw] = h
		}
	}
	var modelDriftGateways []string
	for _, gw := range gateways {
		if oldModelHashes[gw] != newModelHashes[gw] {
			modelDriftGateways = append(modelDriftGateways, gw)
			tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Model hash drift detected", map[string]interface{}{
				"gateway":  gw,
				"old_hash": oldModelHashes[gw],
				"new_hash": newModelHashes[gw],
			})
		}
	}
	if len(modelDriftGateways) > 0 {
		resp.Diagnostics.AddWarning(
			"Model Hash Drift Detected",
			fmt.Sprintf("Gateways with stale models: [%s]\nDeployed API Gateway models don't match schema definitions. Will redeploy via PutRestApi.", strings.Join(modelDriftGateways, ", ")),
		)
		if len(newModelHashes) > 0 {
			mhMap, d := types.MapValueFrom(ctx, types.StringType, newModelHashes)
			resp.Diagnostics.Append(d...)
			plan.ModelHashes = mhMap
		} else {
			plan.ModelHashes = types.MapNull(types.StringType)
		}
		// Mark OpenAPI spec hashes as unknown for affected gateways so Update redeploys them
		oldSpecHashes := make(map[string]string)
		if !state.OpenAPISpecHashes.IsNull() && !state.OpenAPISpecHashes.IsUnknown() {
			state.OpenAPISpecHashes.ElementsAs(ctx, &oldSpecHashes, false)
		}
		driftSet := make(map[string]bool)
		for _, gw := range modelDriftGateways {
			driftSet[gw] = true
		}
		specElements := make(map[string]attr.Value)
		for _, gw := range gateways {
			if driftSet[gw] {
				specElements[gw] = types.StringUnknown()
			} else if h, ok := oldSpecHashes[gw]; ok {
				specElements[gw] = types.StringValue(h)
			}
		}
		plan.OpenAPISpecHashes, _ = types.MapValue(types.StringType, specElements)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	}

	// Mark base_path_mappings as unknown only when gateways are missing mappings
	if !plan.CustomDomainName.IsNull() && !plan.CustomDomainName.IsUnknown() {
		customDomainName := plan.CustomDomainName.ValueString()
		if customDomainName != "" {
			var currentMappings map[string]string
			if !state.BasePathMappings.IsNull() && !state.BasePathMappings.IsUnknown() {
				currentMappings = make(map[string]string)
				state.BasePathMappings.ElementsAs(ctx, &currentMappings, false)
			}
			
			missingMappings := false
			for _, g := range gateways {
				if _, exists := currentMappings[g]; !exists {
					missingMappings = true
					tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Gateway missing base path mapping", map[string]interface{}{
						"gateway": g,
					})
					break
				}
			}
			
			if missingMappings {
				tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Marking base_path_mappings as unknown due to missing mappings")
				plan.BasePathMappings = types.MapUnknown(types.StringType)
				resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
			}
		}
	}

	// Generate OpenAPI specs as a read-only side effect during plan.
	// This lets agents and tools inspect the specs without running a full deploy.
	r.generateSpecsDuringPlan(ctx, &plan, &state, routes, models)
}

// initializeManagers initializes AWS clients and managers for the resource
func (r *dispatcherResource) initializeManagers(ctx context.Context, config *DispatcherConfig) error {
	// Always reinitialize managers with the current config to ensure
	// updated settings (e.g., SharedIamPolicyArns) are picked up.
	// AWS clients are cheap to create and this prevents stale config
	// from Read being used during Update.
	clients, err := initializeClients(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to initialize AWS clients: %w", err)
	}

	r.clients = clients
	r.iamManager = NewIAMManager(clients.IAM, config)
	r.cloudWatchManager = NewCloudWatchManager(clients.CloudWatchLogs, clients.CloudWatch, config)
	r.basePathMappingManager = NewBasePathMappingManager(clients.ApiGateway, config)

	return nil
}

// buildConfigFromModel builds a DispatcherConfig from the resource model
func (r *dispatcherResource) buildConfigFromModel(ctx context.Context, model *DispatcherResourceModel) (*DispatcherConfig, error) {
	config := &DispatcherConfig{
		Environment:            r.providerConfig.Environment,
		AwsRegion:              r.providerConfig.AwsRegion,
		DefaultLambdaTimeout:   r.providerConfig.DefaultLambdaTimeout,
		DefaultLambdaMemory:    r.providerConfig.DefaultLambdaMemory,
		DefaultTags:            r.providerConfig.DefaultTags,
		DockerBuildConcurrency: r.providerConfig.DockerBuildConcurrency,
		AppName:                model.AppName.ValueString(),
		LambdaSourceDir:        model.LambdaSourceDir.ValueString(),
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

	// Extract shared IAM policy ARNs
	if !model.SharedIamPolicyArns.IsNull() && !model.SharedIamPolicyArns.IsUnknown() {
		var policyArns []string
		diags := model.SharedIamPolicyArns.ElementsAs(ctx, &policyArns, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract shared_iam_policy_arns")
		}
		config.SharedIamPolicyArns = policyArns
	}

	// Extract Lambda layer ARNs
	if !model.LambdaLayerArns.IsNull() && !model.LambdaLayerArns.IsUnknown() {
		var layerArns []string
		diags := model.LambdaLayerArns.ElementsAs(ctx, &layerArns, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract lambda_layer_arns")
		}
		config.LambdaLayerArns = layerArns
	}

	// Extract shared directories
	if !model.LambdaSharedDirs.IsNull() && !model.LambdaSharedDirs.IsUnknown() {
		var sharedDirs []string
		diags := model.LambdaSharedDirs.ElementsAs(ctx, &sharedDirs, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract lambda_shared_dirs")
		}
		config.LambdaSharedDirs = sharedDirs
	}

	// Extract gem directories for Docker build context
	if !model.LambdaGemDirs.IsNull() && !model.LambdaGemDirs.IsUnknown() {
		var gemDirs []string
		diags := model.LambdaGemDirs.ElementsAs(ctx, &gemDirs, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract lambda_gem_dirs")
		}
		config.LambdaGemDirs = gemDirs
	}

	// Extract read-only tables
	if !model.ReadOnlyTables.IsNull() && !model.ReadOnlyTables.IsUnknown() {
		var tables []string
		diags := model.ReadOnlyTables.ElementsAs(ctx, &tables, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract read_only_tables")
		}
		config.ReadOnlyTables = tables
	}

	// Extract read-write tables
	if !model.ReadWriteTables.IsNull() && !model.ReadWriteTables.IsUnknown() {
		var tables []string
		diags := model.ReadWriteTables.ElementsAs(ctx, &tables, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract read_write_tables")
		}
		config.ReadWriteTables = tables
	}

	// Extract lambda_config - critical for IAM policy creation from dynamodb_tables
	lambdaConfig, err := r.extractLambdaConfig(ctx, model)
	if err != nil {
		return nil, fmt.Errorf("failed to extract lambda_config: %w", err)
	}
	config.LambdaConfig = lambdaConfig

	// Extract custom domain name
	if !model.CustomDomainName.IsNull() && !model.CustomDomainName.IsUnknown() {
		config.CustomDomainName = model.CustomDomainName.ValueString()
	}

	// Extract friendly_errors setting (defaults to false)
	if !model.FriendlyErrors.IsNull() && !model.FriendlyErrors.IsUnknown() {
		config.FriendlyErrors = model.FriendlyErrors.ValueBool()
	}

	// Extract suppress_table_env_vars setting (defaults to false)
	if !model.SuppressTableEnvVars.IsNull() && !model.SuppressTableEnvVars.IsUnknown() {
		config.SuppressTableEnvVars = model.SuppressTableEnvVars.ValueBool()
	}

	// Extract schema_source
	if !model.SchemaSource.IsNull() && !model.SchemaSource.IsUnknown() {
		config.SchemaSource = model.SchemaSource.ValueString()
	}

	// Extract alarm_config
	if !model.AlarmConfig.IsNull() && !model.AlarmConfig.IsUnknown() {
		config.AlarmConfig = r.extractAlarmConfig(ctx, model)
	}

	return config, nil
}


// parseRoutes executes belt routes and returns parsed routes
func (r *dispatcherResource) parseRoutes(ctx context.Context, source string) ([]utils.Route, error) {
	return executeBeltRoutes(ctx, source)
}

// parseRoutesAndModels executes belt routes and returns both routes and model definitions.
// If schemaSource is provided, it parses models from that file; otherwise auto-detects
// schema.tf.rb in the same directory as the routes source file.
func (r *dispatcherResource) parseRoutesAndModels(ctx context.Context, source, schemaSource string) ([]utils.Route, []utils.ModelDefinition, error) {
	// Determine schema path: explicit config, or auto-detect from routes file directory
	schemaPath := schemaSource
	if schemaPath == "" {
		absSource := source
		if !filepath.IsAbs(source) {
			if wd, err := os.Getwd(); err == nil {
				absSource = filepath.Join(wd, source)
			}
		}
		candidate := filepath.Join(filepath.Dir(absSource), "schema.tf.rb")
		if _, err := os.Stat(candidate); err == nil {
			schemaPath = candidate
		}
	}

	routes, models, err := executeBeltRoutesWithSchema(ctx, source, schemaPath)
	if err != nil {
		return nil, nil, err
	}

	return routes, models, nil
}

// extractAlarmConfig extracts alarm_config from the Terraform model into an AlarmConfig struct
func (r *dispatcherResource) extractAlarmConfig(ctx context.Context, model *DispatcherResourceModel) *AlarmConfig {
	if model.AlarmConfig.IsNull() || model.AlarmConfig.IsUnknown() {
		return nil
	}

	attrs := model.AlarmConfig.Attributes()
	ac := &AlarmConfig{
		// Defaults
		ErrorEnabled:                 true,
		ErrorThreshold:               1,
		ErrorPeriod:                  60,
		ErrorEvaluationPeriods:       1,
		ErrorStatistic:               "Sum",
		DurationEnabled:              false,
		DurationThreshold:            10000,
		DurationPeriod:               60,
		DurationEvaluationPeriods:    1,
		DurationStatistic:            "Maximum",
		ThrottleEnabled:              false,
		ThrottleThreshold:            1,
		ThrottlePeriod:               60,
		ThrottleEvaluationPeriods:    1,
		ThrottleStatistic:            "Sum",
		InvocationsEnabled:           false,
		InvocationsThreshold:         1000,
		InvocationsPeriod:            300,
		InvocationsEvaluationPeriods: 1,
		InvocationsStatistic:         "Sum",
		LambdaOverrides:             make(map[string]*LambdaAlarmConfig),
	}

	if v, ok := attrs["enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
		ac.Enabled = v.ValueBool()
	}
	if v, ok := attrs["sns_topic_arn"].(types.String); ok && !v.IsNull() && !v.IsUnknown() {
		ac.SnsTopicArn = v.ValueString()
	}
	if v, ok := attrs["error_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ErrorEnabled = v.ValueBool()
	}
	if v, ok := attrs["error_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ErrorThreshold = v.ValueInt64()
	}
	if v, ok := attrs["error_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ErrorPeriod = v.ValueInt64()
	}
	if v, ok := attrs["error_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ErrorEvaluationPeriods = v.ValueInt64()
	}
	if v, ok := attrs["duration_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
		ac.DurationEnabled = v.ValueBool()
	}
	if v, ok := attrs["duration_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.DurationThreshold = v.ValueInt64()
	}
	if v, ok := attrs["duration_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.DurationPeriod = v.ValueInt64()
	}
	if v, ok := attrs["duration_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.DurationEvaluationPeriods = v.ValueInt64()
	}
	if v, ok := attrs["throttle_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ThrottleEnabled = v.ValueBool()
	}
	if v, ok := attrs["throttle_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ThrottleThreshold = v.ValueInt64()
	}
	if v, ok := attrs["throttle_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ThrottlePeriod = v.ValueInt64()
	}
	if v, ok := attrs["throttle_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		ac.ThrottleEvaluationPeriods = v.ValueInt64()
	}

	// Extract lambda_overrides (Dynamic attribute containing per-lambda overrides)
	if overridesVal, ok := attrs["lambda_overrides"].(types.Dynamic); ok && !overridesVal.IsNull() && !overridesVal.IsUnknown() {
		underlyingVal := overridesVal.UnderlyingValue()
		if objVal, ok := underlyingVal.(types.Object); ok && !objVal.IsNull() && !objVal.IsUnknown() {
			for lambdaName, lambdaOverrideVal := range objVal.Attributes() {
				if lambdaObj, ok := lambdaOverrideVal.(types.Object); ok && !lambdaObj.IsNull() && !lambdaObj.IsUnknown() {
					override := &LambdaAlarmConfig{}
					overrideAttrs := lambdaObj.Attributes()

					if v, ok := overrideAttrs["error_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueBool()
						override.ErrorEnabled = &val
					}
					if v, ok := overrideAttrs["error_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.ErrorThreshold = &val
					}
					if v, ok := overrideAttrs["error_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.ErrorPeriod = &val
					}
					if v, ok := overrideAttrs["error_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.ErrorEvaluationPeriods = &val
					}
					if v, ok := overrideAttrs["duration_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueBool()
						override.DurationEnabled = &val
					}
					if v, ok := overrideAttrs["duration_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.DurationThreshold = &val
					}
					if v, ok := overrideAttrs["duration_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.DurationPeriod = &val
					}
					if v, ok := overrideAttrs["duration_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.DurationEvaluationPeriods = &val
					}
					if v, ok := overrideAttrs["throttle_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueBool()
						override.ThrottleEnabled = &val
					}
					if v, ok := overrideAttrs["throttle_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.ThrottleThreshold = &val
					}
					if v, ok := overrideAttrs["throttle_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.ThrottlePeriod = &val
					}
					if v, ok := overrideAttrs["throttle_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.ThrottleEvaluationPeriods = &val
					}
					if v, ok := overrideAttrs["invocations_enabled"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueBool()
						override.InvocationsEnabled = &val
					}
					if v, ok := overrideAttrs["invocations_threshold"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.InvocationsThreshold = &val
					}
					if v, ok := overrideAttrs["invocations_period"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.InvocationsPeriod = &val
					}
					if v, ok := overrideAttrs["invocations_evaluation_periods"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
						val := v.ValueInt64()
						override.InvocationsEvaluationPeriods = &val
					}

					ac.LambdaOverrides[lambdaName] = override
				}
			}
		}
	}

	return ac
}

// extractResources extracts unique lambda and gateway names from routes, lambda_config, and lambda_source_dir
func (r *dispatcherResource) extractResources(routes []utils.Route, lambdaConfig map[string]interface{}) (lambdas []string, gateways []string) {
	lambdaSet := make(map[string]bool)
	gatewaySet := make(map[string]bool)

	// From routes
	for _, route := range routes {
		if route.Lambda != "" {
			lambdaSet[route.Lambda] = true
		}
		if route.Gateway != "" {
			gatewaySet[route.Gateway] = true
		}
	}

	// Standalone lambdas from lambda_config (lambdas not in routes)
	for lambda := range lambdaConfig {
		if lambda != "shared" {
			lambdaSet[lambda] = true
		}
	}

	// Convert to sorted slices for deterministic ordering
	lambdas = make([]string, 0, len(lambdaSet))
	for l := range lambdaSet {
		lambdas = append(lambdas, l)
	}

	gateways = make([]string, 0, len(gatewaySet))
	for g := range gatewaySet {
		gateways = append(gateways, g)
	}

	// Sort for deterministic ordering
	sort.Strings(lambdas)
	sort.Strings(gateways)

	return lambdas, gateways
}

// extractResourcesWithSourceDir extracts unique lambda and gateway names from routes, lambda_config, and lambda_source_dir
// This version also scans the lambda_source_dir for Ruby files to discover standalone lambdas
func (r *dispatcherResource) extractResourcesWithSourceDir(routes []utils.Route, lambdaConfig map[string]interface{}, lambdaSourceDir string) (lambdas []string, gateways []string) {
	lambdaSet := make(map[string]bool)
	gatewaySet := make(map[string]bool)

	// From routes
	for _, route := range routes {
		if route.Lambda != "" {
			lambdaSet[route.Lambda] = true
		}
		if route.Gateway != "" {
			gatewaySet[route.Gateway] = true
		}
	}

	// Standalone lambdas from lambda_config (lambdas not in routes)
	for lambda := range lambdaConfig {
		if lambda != "shared" {
			lambdaSet[lambda] = true
		}
	}

	// Discover lambdas from Ruby files in lambda_source_dir
	if lambdaSourceDir != "" {
		discoveredLambdas := discoverLambdasFromSourceDir(lambdaSourceDir)
		for _, lambda := range discoveredLambdas {
			lambdaSet[lambda] = true
		}
	}

	// Convert to sorted slices for deterministic ordering
	lambdas = make([]string, 0, len(lambdaSet))
	for l := range lambdaSet {
		lambdas = append(lambdas, l)
	}

	gateways = make([]string, 0, len(gatewaySet))
	for g := range gatewaySet {
		gateways = append(gateways, g)
	}

	// Sort for deterministic ordering
	sort.Strings(lambdas)
	sort.Strings(gateways)

	return lambdas, gateways
}

// discoverLambdasFromSourceDir scans the lambda source directory for Ruby files
// and returns the lambda names (filename without .rb extension)
func discoverLambdasFromSourceDir(sourceDir string) []string {
	var lambdas []string

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		// If we can't read the directory, just return empty
		// The error will be caught later during package building
		return lambdas
	}

	for _, entry := range entries {
		// Only look at files (not directories) with .rb extension
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, ".rb") {
			// Extract lambda name by removing .rb extension
			lambdaName := strings.TrimSuffix(name, ".rb")
			lambdas = append(lambdas, lambdaName)
		}
	}

	return lambdas
}

// extractLambdaConfig extracts lambda_config from the model as a map
func (r *dispatcherResource) extractLambdaConfig(ctx context.Context, model *DispatcherResourceModel) (map[string]interface{}, error) {
	if model.LambdaConfig.IsNull() || model.LambdaConfig.IsUnknown() {
		return make(map[string]interface{}), nil
	}

	// Get the underlying value from the Dynamic type
	underlyingValue := model.LambdaConfig.UnderlyingValue()
	if underlyingValue == nil {
		return make(map[string]interface{}), nil
	}

	// Check if it's a map type
	mapValue, ok := underlyingValue.(types.Map)
	if !ok {
		// Try to handle as object type
		objValue, ok := underlyingValue.(types.Object)
		if !ok {
			return nil, fmt.Errorf("lambda_config must be a map or object, got %T", underlyingValue)
		}
		// Convert object to map
		lambdaConfig := make(map[string]interface{})
		for key, val := range objValue.Attributes() {
			lambdaConfig[key] = val
		}
		return lambdaConfig, nil
	}

	lambdaConfig := make(map[string]interface{})
	diags := mapValue.ElementsAs(ctx, &lambdaConfig, false)
	if diags.HasError() {
		return nil, fmt.Errorf("failed to extract lambda_config")
	}

	return lambdaConfig, nil
}

// Create creates the resource and sets the initial Terraform state.
func (r *dispatcherResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DispatcherResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if r.client != nil {
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

	utils.Info(ctx, "Creating dispatcher resource", map[string]interface{}{
		"app_name":    config.AppName,
		"environment": config.Environment,
		"source":      plan.Source.ValueString(),
	})

	// Step 1: Parse routes and models from source file
	routes, models, err := r.parseRoutesAndModels(ctx, plan.Source.ValueString(), config.SchemaSource)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to parse routes",
			"Could not parse routes from source file: "+err.Error(),
		)
		return
	}

	utils.Info(ctx, "Parsed routes", map[string]interface{}{
		"route_count": len(routes),
		"model_count": len(models),
	})

	// Step 2: Extract lambda_config
	lambdaConfig, err := r.extractLambdaConfig(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to extract lambda_config",
			err.Error(),
		)
		return
	}

	// Step 3: Extract unique lambdas and gateways (including from source directory)
	lambdas, gateways := r.extractResourcesWithSourceDir(routes, lambdaConfig, config.LambdaSourceDir)

	utils.Info(ctx, "Extracted resources", map[string]interface{}{
		"lambda_count":  len(lambdas),
		"gateway_count": len(gateways),
		"lambdas":       lambdas,
		"gateways":      gateways,
	})

	// Step 4: Build Lambda packages in parallel
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	packageBuilder := NewPackageBuilder(
		config.LambdaSourceDir,
		WithSharedDirs(sharedDirs),
		WithGemDirs(config.LambdaGemDirs),
		WithConcurrency(config.DockerBuildConcurrency),
		WithConfig(config),
	)

	utils.Info(ctx, "Building Lambda packages in parallel", map[string]interface{}{
		"lambda_count": len(lambdas),
		"concurrency":  packageBuilder.Concurrency(),
	})

	buildResults := packageBuilder.BuildPackages(ctx, lambdas)

	// Check for build failures
	if packageBuilder.HasFailures(buildResults) {
		resp.Diagnostics.AddError(
			"Failed to build Lambda packages",
			packageBuilder.GetFailureErrors(buildResults).Error(),
		)
		return
	}

	// Step 5: Create Lambdas in parallel using ParallelManager
	parallelManager := NewParallelManager(config.DockerBuildConcurrency, r.clients, config)

	lambdaResults := parallelManager.CreateLambdasInParallel(ctx, lambdas, buildResults, routes, lambdaConfig, r.iamManager, r.cloudWatchManager)

	// Collect Lambda ARNs and check for failures
	lambdaARNs := make(map[string]string)
	var createErrors []string
	for _, result := range lambdaResults {
		if result.Error != nil {
			createErrors = append(createErrors, fmt.Sprintf("%s: %s", result.Action, result.Error.Error()))
		} else {
			lambdaARNs[result.Action] = result.ARN
		}
	}

	if len(createErrors) > 0 {
		resp.Diagnostics.AddError(
			"Failed to create Lambda functions",
			fmt.Sprintf("Failed to create %d Lambda functions: %v", len(createErrors), createErrors),
		)
		return
	}

	utils.Info(ctx, "Created Lambda functions", map[string]interface{}{
		"count": len(lambdaARNs),
	})

	// Step 6: Create API Gateways
	gatewayResults := parallelManager.CreateGatewaysInParallel(ctx, gateways, routes, lambdaARNs, config, models)

	// Collect Gateway IDs/URLs and check for failures
	gatewayIDs := make(map[string]string)
	gatewayURLs := make(map[string]string)
	var gatewayErrors []string
	for _, result := range gatewayResults {
		if result.Error != nil {
			gatewayErrors = append(gatewayErrors, fmt.Sprintf("%s: %s", result.Gateway, result.Error.Error()))
		} else {
			gatewayIDs[result.Gateway] = result.ID
			gatewayURLs[result.Gateway] = result.URL
		}
	}

	if len(gatewayErrors) > 0 {
		resp.Diagnostics.AddError(
			"Failed to create API Gateways",
			fmt.Sprintf("Failed to create %d API Gateways: %v", len(gatewayErrors), gatewayErrors),
		)
		return
	}

	utils.Info(ctx, "Created API Gateways", map[string]interface{}{
		"count": len(gatewayIDs),
	})

	// Step 6.5: Create base path mappings if custom_domain_name is provided
	basePathMappings := make(map[string]string)
	customDomainUrl := ""

	if !plan.CustomDomainName.IsNull() && !plan.CustomDomainName.IsUnknown() {
		customDomainName := plan.CustomDomainName.ValueString()
		if customDomainName != "" {
			utils.Info(ctx, "Creating base path mappings for custom domain", map[string]interface{}{
				"custom_domain_name": customDomainName,
				"gateway_count":      len(gatewayIDs),
			})

			// Validate custom domain exists
			if err := r.basePathMappingManager.ValidateCustomDomainExists(ctx, customDomainName); err != nil {
				resp.Diagnostics.AddError(
					"Custom Domain Validation Failed",
					err.Error(),
				)
				return
			}

			// Create base path mappings for each gateway
			stageName := config.Environment
			mappings, err := r.basePathMappingManager.SyncBasePathMappings(ctx, customDomainName, gatewayIDs, stageName)
			if err != nil {
				resp.Diagnostics.AddWarning(
					"Base Path Mapping Warning",
					fmt.Sprintf("Some base path mappings could not be created: %s", err.Error()),
				)
			}

			// Store successful mappings
			for gateway := range gatewayIDs {
				basePathMappings[gateway] = gateway // base path equals gateway name
			}

			// Update mappings with actual results
			if mappings != nil {
				for basePath := range mappings {
					basePathMappings[basePath] = basePath
				}
			}

			// Build custom domain URL
			customDomainUrl = r.basePathMappingManager.BuildCustomDomainURL(customDomainName)

			utils.Info(ctx, "Created base path mappings", map[string]interface{}{
				"custom_domain_url": customDomainUrl,
				"mapping_count":     len(basePathMappings),
			})
		}
	}

	// Step 7: Calculate hashes for change detection
	routesHash, _ := calculateConfigHash(routes, models, lambdaConfig, config.ReadOnlyTables, config.ReadWriteTables)
	lambdaHashes := r.calculateLambdaHashes(ctx, lambdas, routes, lambdaConfig, config)
	gatewayHashes, _ := calculateAllGatewayHashes(routes, config.FrontendUrls)

	// Calculate model hashes per gateway
	modelHashesMap := make(map[string]string)
	for _, gateway := range gateways {
		gatewayModels := GetModelsForGateway(routes, models, gateway)
		if h, err := CalculateModelHash(gatewayModels); err == nil && h != "" {
			modelHashesMap[gateway] = h
		}
	}

	// Step 7.5: Generate OpenAPI specs as read-only side effect (Phase 1)
	// This generates specs and stores hashes for future comparison, but does NOT
	// change deployment behavior. The imperative path above is still the active deployer.
	openAPISpecHashes := r.generateOpenAPISpecsSideEffect(ctx, routes, lambdaARNs, models, config)


	// Step 8: Populate computed outputs
	plan.ID = types.StringValue(fmt.Sprintf("%s-%s", config.AppName, config.Environment))

	// Plan-time known outputs (also set during ModifyPlan, but must be consistent here)
	gatewayNamesList, diags := types.ListValueFrom(ctx, types.StringType, gateways)
	resp.Diagnostics.Append(diags...)
	plan.GatewayNames = gatewayNamesList

	lambdaNamesList, diags := types.ListValueFrom(ctx, types.StringType, lambdas)
	resp.Diagnostics.Append(diags...)
	plan.LambdaNames = lambdaNamesList

	// Convert maps to Terraform types
	lambdaFunctionsMap, diags := types.MapValueFrom(ctx, types.StringType, lambdaARNs)
	resp.Diagnostics.Append(diags...)
	plan.LambdaFunctions = lambdaFunctionsMap

	gatewayIDsMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayIDs)
	resp.Diagnostics.Append(diags...)
	plan.ApiGatewayIds = gatewayIDsMap

	gatewayURLsMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayURLs)
	resp.Diagnostics.Append(diags...)
	plan.ApiGatewayUrls = gatewayURLsMap

	plan.RoutesHash = types.StringValue(routesHash)

	// Store routes JSON for change detection
	routesJson, err := json.Marshal(routes)
	if err != nil {
		resp.Diagnostics.AddError("Failed to serialize routes", err.Error())
		return
	}
	plan.RoutesJson = types.StringValue(string(routesJson))

	lambdaHashesMap, diags := types.MapValueFrom(ctx, types.StringType, lambdaHashes)
	resp.Diagnostics.Append(diags...)
	plan.LambdaHashes = lambdaHashesMap

	// Store separate source/config hashes for precise change detection
	sourceHashes, configHashes := r.calculateSeparateLambdaHashes(ctx, lambdas, routes, lambdaConfig, config)
	sourceHashesMap, diags := types.MapValueFrom(ctx, types.StringType, sourceHashes)
	resp.Diagnostics.Append(diags...)
	plan.LambdaSourceHashes = sourceHashesMap
	configHashesMap, diags := types.MapValueFrom(ctx, types.StringType, configHashes)
	resp.Diagnostics.Append(diags...)
	plan.LambdaConfigHashes = configHashesMap

	gatewayHashesMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayHashes)
	resp.Diagnostics.Append(diags...)
	plan.GatewayHashes = gatewayHashesMap

	// Set base path mappings and custom domain URL
	basePathMappingsMap, diags := types.MapValueFrom(ctx, types.StringType, basePathMappings)
	resp.Diagnostics.Append(diags...)
	plan.BasePathMappings = basePathMappingsMap

	if customDomainUrl != "" {
		plan.CustomDomainUrl = types.StringValue(customDomainUrl)
	} else {
		plan.CustomDomainUrl = types.StringNull()
	}

	modelHashesTfMap, diags := types.MapValueFrom(ctx, types.StringType, modelHashesMap)
	resp.Diagnostics.Append(diags...)
	plan.ModelHashes = modelHashesTfMap

	openAPISpecHashesTfMap, diags := types.MapValueFrom(ctx, types.StringType, openAPISpecHashes)
	resp.Diagnostics.Append(diags...)
	plan.OpenAPISpecHashes = openAPISpecHashesTfMap

	if resp.Diagnostics.HasError() {
		return
	}

	utils.Info(ctx, "Successfully created dispatcher resource", map[string]interface{}{
		"id":             plan.ID.ValueString(),
		"lambda_count":   len(lambdaARNs),
		"gateway_count":  len(gatewayIDs),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// calculateLambdaHashes calculates hashes for each Lambda lambda
func (r *dispatcherResource) calculateLambdaHashes(ctx context.Context, lambdas []string, routes []utils.Route, lambdaConfig map[string]interface{}, config *DispatcherConfig) map[string]string {
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}
	hashes, err := calculateAllLambdaHashes(lambdas, routes, lambdaConfig, config.LambdaSourceDir, sharedDirs, config.LambdaLayerArns, config.AlarmConfig, config.ReadOnlyTables, config.ReadWriteTables, config.SharedIamPolicyArns)
	if err != nil {
		utils.Warn(ctx, "Failed to calculate lambda hashes", map[string]interface{}{"error": err.Error()})
		return make(map[string]string)
	}
	return hashes
}

// calculateSeparateLambdaHashes calculates source and config hashes separately
// for precise change detection (avoids Docker builds for config-only changes)
func (r *dispatcherResource) calculateSeparateLambdaHashes(ctx context.Context, lambdas []string, routes []utils.Route, lambdaConfig map[string]interface{}, config *DispatcherConfig) (sourceHashes, configHashes map[string]string) {
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	sourceHashes = make(map[string]string, len(lambdas))

	for _, lambda := range lambdas {
		if sh, err := calculateLambdaSourceHash(config.LambdaSourceDir, lambda, sharedDirs, config.LambdaGemDirs...); err == nil {
			sourceHashes[lambda] = sh
		}
	}

	// Delegate to calculateAllLambdaConfigHashes — the same function ModifyPlan uses —
	// to guarantee identical hashes between plan and apply. Using a different code path
	// (e.g., calling calculateLambdaConfigHash directly with all routes) can produce
	// inconsistent results due to subtle differences in how routes are filtered.
	var err error
	configHashes, err = calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, config.LambdaLayerArns, config.AlarmConfig, config.ReadOnlyTables, config.ReadWriteTables, config.SharedIamPolicyArns)
	if err != nil {
		utils.Warn(ctx, "Failed to calculate config hashes, falling back to empty", map[string]interface{}{"error": err.Error()})
		configHashes = make(map[string]string, len(lambdas))
	}
	return sourceHashes, configHashes
}


// Read reads the resource state from AWS.
func (r *dispatcherResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DispatcherResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// in Create/Update/Delete or by the OS via os.TempDir().

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

	utils.Info(ctx, "Reading dispatcher resource", map[string]interface{}{
		"id": state.ID.ValueString(),
	})

	// After import, source is not yet in state (it comes from config on next plan/apply).
	// Skip route parsing and return state as-is. ModifyPlan will handle resource discovery
	// once it has access to the parsed routes from the plan config.
	if state.Source.IsNull() || state.Source.IsUnknown() || state.Source.ValueString() == "" {
		utils.Info(ctx, "Source is empty (likely post-import), skipping route parsing", nil)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// Re-parse routes and models to get current state
	schemaSource := ""
	if !state.SchemaSource.IsNull() && !state.SchemaSource.IsUnknown() {
		schemaSource = state.SchemaSource.ValueString()
	}
	routes, models, err := r.parseRoutesAndModels(ctx, state.Source.ValueString(), schemaSource)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to parse routes",
			"Could not parse routes from source file: "+err.Error(),
		)
		return
	}

	// Extract lambda_config
	lambdaConfig, err := r.extractLambdaConfig(ctx, &state)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to extract lambda_config",
			err.Error(),
		)
		return
	}

	// Extract unique lambdas and gateways from routes (including from source directory)
	lambdas, gateways := r.extractResourcesWithSourceDir(routes, lambdaConfig, config.LambdaSourceDir)

	// Initialize parallel manager for reading state
	parallelManager := NewParallelManager(config.DockerBuildConcurrency, r.clients, config)

	// Read Lambda state from AWS
	lambdaStateResults := parallelManager.ReadLambdasInParallel(ctx, lambdas)

	// Build lambda_functions map from AWS state
	// Start with existing state values to preserve them if not found in AWS
	lambdaARNs := make(map[string]string)
	if !state.LambdaFunctions.IsNull() && !state.LambdaFunctions.IsUnknown() {
		state.LambdaFunctions.ElementsAs(ctx, &lambdaARNs, false)
	}
	
	var missingLambdas []string
	for _, result := range lambdaStateResults {
		if result.Error != nil {
			utils.Warn(ctx, "Error reading Lambda state", map[string]interface{}{
				"lambda": result.Action,
				"error":  result.Error.Error(),
			})
			continue
		}
		if result.Exists {
			lambdaARNs[result.Action] = result.ARN
		} else {
			missingLambdas = append(missingLambdas, result.Action)
		}
	}

	if len(missingLambdas) > 0 {
		utils.Warn(ctx, "Some Lambda functions not found in AWS", map[string]interface{}{
			"missing_lambdas": missingLambdas,
		})
	}

	// Detect env var drift: compare actual AWS env vars with expected config.
	// If a lambda is missing expected env vars, clear its config hash in state
	// so that Plan detects the mismatch and triggers an UpdateFunctionConfiguration.
	oldConfigHashes := make(map[string]string)
	if !state.LambdaConfigHashes.IsNull() && !state.LambdaConfigHashes.IsUnknown() {
		state.LambdaConfigHashes.ElementsAs(ctx, &oldConfigHashes, false)
	}
	oldLambdaHashes := make(map[string]string)
	if !state.LambdaHashes.IsNull() && !state.LambdaHashes.IsUnknown() {
		state.LambdaHashes.ElementsAs(ctx, &oldLambdaHashes, false)
	}
	envVarDriftDetected := false
	for _, result := range lambdaStateResults {
		if !result.Exists || result.Error != nil {
			continue
		}

		// Build expected env vars for this lambda
		expectedEnvVars := parallelManager.buildEnvVars(result.Action, routes, lambdaConfig)

		// Check if any expected env var is missing or has wrong value in AWS
		for key, expectedVal := range expectedEnvVars {
			// Skip TABLES — it's a comma-separated list built from map iteration
			// (non-deterministic order) so it will always differ in ordering
			if key == "TABLES" {
				continue
			}
			actualVal, exists := result.EnvVars[key]
			if !exists || actualVal != expectedVal {
				utils.Warn(ctx, "Env var drift detected — clearing config hash to force update", map[string]interface{}{
					"lambda":  result.Action,
					"key":     key,
					"missing": !exists,
				})
				delete(oldConfigHashes, result.Action)
				delete(oldLambdaHashes, result.Action)
				envVarDriftDetected = true
				break // One mismatch is enough to trigger update for this lambda
			}
		}
	}

	if envVarDriftDetected {
		// Write updated hashes back to state
		if len(oldConfigHashes) > 0 {
			chMap, d := types.MapValueFrom(ctx, types.StringType, oldConfigHashes)
			resp.Diagnostics.Append(d...)
			state.LambdaConfigHashes = chMap
		} else {
			state.LambdaConfigHashes = types.MapNull(types.StringType)
		}
		if len(oldLambdaHashes) > 0 {
			lhMap, d := types.MapValueFrom(ctx, types.StringType, oldLambdaHashes)
			resp.Diagnostics.Append(d...)
			state.LambdaHashes = lhMap
		} else {
			state.LambdaHashes = types.MapNull(types.StringType)
		}
	}

	// Read Gateway state from AWS
	gatewayStateResults := parallelManager.ReadGatewaysInParallel(ctx, gateways)

	// Build gateway maps from AWS state
	// Start with existing state values to preserve them if not found in AWS
	gatewayIDs := make(map[string]string)
	gatewayURLs := make(map[string]string)
	if !state.ApiGatewayIds.IsNull() && !state.ApiGatewayIds.IsUnknown() {
		state.ApiGatewayIds.ElementsAs(ctx, &gatewayIDs, false)
	}
	if !state.ApiGatewayUrls.IsNull() && !state.ApiGatewayUrls.IsUnknown() {
		state.ApiGatewayUrls.ElementsAs(ctx, &gatewayURLs, false)
	}
	
	var missingGateways []string
	for _, result := range gatewayStateResults {
		if result.Error != nil {
			utils.Warn(ctx, "Error reading Gateway state", map[string]interface{}{
				"gateway": result.Gateway,
				"error":   result.Error.Error(),
			})
			continue
		}
		if result.Exists {
			gatewayIDs[result.Gateway] = result.ID
			gatewayURLs[result.Gateway] = result.URL
		} else {
			missingGateways = append(missingGateways, result.Gateway)
		}
	}

	if len(missingGateways) > 0 {
		utils.Warn(ctx, "Some API Gateways not found in AWS", map[string]interface{}{
			"missing_gateways": missingGateways,
		})
	}

	// Update state with actual AWS values
	lambdaFunctionsMap, diags := types.MapValueFrom(ctx, types.StringType, lambdaARNs)
	resp.Diagnostics.Append(diags...)
	state.LambdaFunctions = lambdaFunctionsMap

	gatewayIDsMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayIDs)
	resp.Diagnostics.Append(diags...)
	state.ApiGatewayIds = gatewayIDsMap

	gatewayURLsMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayURLs)
	resp.Diagnostics.Append(diags...)
	state.ApiGatewayUrls = gatewayURLsMap

	// NOTE: We deliberately DO NOT recalculate route/lambda hashes in Read.
	// Those hashes should only be updated during Create/Update operations.
	// However, we DO check for model hash drift — if the models actually deployed
	// on AWS don't match what the routes file expects, we clear the model hash
	// in state so that Plan detects the mismatch and triggers an update.
	// This fixes state poisoning from v0.32.0 where model hashes were written
	// to state without PutRestApi actually deploying them.
	oldModelHashes := make(map[string]string)
	if !state.ModelHashes.IsNull() && !state.ModelHashes.IsUnknown() {
		state.ModelHashes.ElementsAs(ctx, &oldModelHashes, false)
	}
	driftDetected := false
	for gateway, apiID := range gatewayIDs {
		if apiID == "" {
			continue
		}
		deployedHash, err := deployedModelFingerprint(ctx, r.clients.ApiGateway, apiID)
		if err != nil {
			utils.Warn(ctx, "Failed to fetch deployed models for drift detection", map[string]interface{}{
				"gateway": gateway, "api_id": apiID, "error": err.Error(),
			})
			continue
		}
		gwModels := GetModelsForGateway(routes, models, gateway)
		expectedHash, err := expectedDeployedModelFingerprint(gwModels)
		if err != nil {
			utils.Warn(ctx, "Failed to compute expected model fingerprint", map[string]interface{}{
				"gateway": gateway, "error": err.Error(),
			})
			continue
		}
		if deployedHash != expectedHash {
			utils.Warn(ctx, "Model drift detected — deployed models don't match expected", map[string]interface{}{
				"gateway": gateway, "deployed_hash": deployedHash, "expected_hash": expectedHash,
			})
			// Clear the model hash so Plan sees a diff and triggers PutRestApi
			delete(oldModelHashes, gateway)
			driftDetected = true
		}
	}
	if driftDetected {
		utils.Info(ctx, "Model drift detected, clearing affected model hashes in state", nil)
		if len(oldModelHashes) > 0 {
			mhMap, d := types.MapValueFrom(ctx, types.StringType, oldModelHashes)
			resp.Diagnostics.Append(d...)
			state.ModelHashes = mhMap
		} else {
			state.ModelHashes = types.MapNull(types.StringType)
		}
	}

	// Read base path mappings from AWS for drift detection
	basePathMappings := make(map[string]string)
	customDomainUrl := ""

	if !state.CustomDomainName.IsNull() && !state.CustomDomainName.IsUnknown() {
		customDomainName := state.CustomDomainName.ValueString()
		if customDomainName != "" {
			utils.Info(ctx, "Reading base path mappings for drift detection", map[string]interface{}{
				"custom_domain_name": customDomainName,
			})

			// Get current mappings from AWS
			awsMappings, err := r.basePathMappingManager.GetBasePathMappings(ctx, customDomainName)
			if err != nil {
				utils.Warn(ctx, "Failed to read base path mappings from AWS", map[string]interface{}{
					"custom_domain_name": customDomainName,
					"error":              err.Error(),
				})
				// Preserve existing state if we can't read from AWS
				if !state.BasePathMappings.IsNull() && !state.BasePathMappings.IsUnknown() {
					state.BasePathMappings.ElementsAs(ctx, &basePathMappings, false)
				}
			} else {
				// Filter to only include mappings for our gateways
				for basePath, apiId := range awsMappings {
					// Check if this mapping belongs to one of our gateways
					if expectedApiId, exists := gatewayIDs[basePath]; exists {
						if apiId == expectedApiId {
							basePathMappings[basePath] = basePath
						} else {
							// Drift detected - mapping points to different API Gateway
							utils.Warn(ctx, "Base path mapping drift detected", map[string]interface{}{
								"base_path":       basePath,
								"expected_api_id": expectedApiId,
								"actual_api_id":   apiId,
							})
							// Still include it in state but log the drift
							basePathMappings[basePath] = basePath
						}
					}
				}

				// Check for missing mappings (mappings that should exist but don't)
				for gateway := range gatewayIDs {
					if _, exists := awsMappings[gateway]; !exists {
						utils.Warn(ctx, "Base path mapping missing in AWS", map[string]interface{}{
							"gateway":            gateway,
							"custom_domain_name": customDomainName,
						})
					}
				}
			}

			// Build custom domain URL
			customDomainUrl = r.basePathMappingManager.BuildCustomDomainURL(customDomainName)
		}
	}

	// Update base path mappings in state
	basePathMappingsMap, diags := types.MapValueFrom(ctx, types.StringType, basePathMappings)
	resp.Diagnostics.Append(diags...)
	state.BasePathMappings = basePathMappingsMap

	if customDomainUrl != "" {
		state.CustomDomainUrl = types.StringValue(customDomainUrl)
	} else {
		state.CustomDomainUrl = types.StringNull()
	}

	utils.Info(ctx, "Successfully read dispatcher resource state", map[string]interface{}{
		"id":            state.ID.ValueString(),
		"lambda_count":  len(lambdaARNs),
		"gateway_count": len(gatewayIDs),
	})

	// Set plan-time known attributes from parsed routes
	gatewayNamesList, diags := types.ListValueFrom(ctx, types.StringType, gateways)
	resp.Diagnostics.Append(diags...)
	state.GatewayNames = gatewayNamesList

	lambdaNamesList, diags := types.ListValueFrom(ctx, types.StringType, lambdas)
	resp.Diagnostics.Append(diags...)
	state.LambdaNames = lambdaNamesList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state.
func (r *dispatcherResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DispatcherResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if r.client != nil {
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

	utils.Info(ctx, "Updating dispatcher resource", map[string]interface{}{
		"id": state.ID.ValueString(),
	})

	// Parse new routes and models
	routes, models, err := r.parseRoutesAndModels(ctx, plan.Source.ValueString(), config.SchemaSource)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to parse routes",
			"Could not parse routes from source file: "+err.Error(),
		)
		return
	}

	// Extract lambda_config
	lambdaConfig, err := r.extractLambdaConfig(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to extract lambda_config",
			err.Error(),
		)
		return
	}

	// Extract current lambdas and gateways (including from source directory)
	newLambdas, newGateways := r.extractResourcesWithSourceDir(routes, lambdaConfig, config.LambdaSourceDir)

	// Get previous state
	var oldLambdaARNs map[string]string
	if !state.LambdaFunctions.IsNull() && !state.LambdaFunctions.IsUnknown() {
		oldLambdaARNs = make(map[string]string)
		state.LambdaFunctions.ElementsAs(ctx, &oldLambdaARNs, false)
	}

	var oldGatewayIDs map[string]string
	if !state.ApiGatewayIds.IsNull() && !state.ApiGatewayIds.IsUnknown() {
		oldGatewayIDs = make(map[string]string)
		state.ApiGatewayIds.ElementsAs(ctx, &oldGatewayIDs, false)
	}

	var oldLambdaHashes map[string]string
	if !state.LambdaHashes.IsNull() && !state.LambdaHashes.IsUnknown() {
		oldLambdaHashes = make(map[string]string)
		state.LambdaHashes.ElementsAs(ctx, &oldLambdaHashes, false)
	}

	var oldGatewayHashes map[string]string
	if !state.GatewayHashes.IsNull() && !state.GatewayHashes.IsUnknown() {
		oldGatewayHashes = make(map[string]string)
		state.GatewayHashes.ElementsAs(ctx, &oldGatewayHashes, false)
	}

	var oldModelHashes map[string]string
	if !state.ModelHashes.IsNull() && !state.ModelHashes.IsUnknown() {
		oldModelHashes = make(map[string]string)
		state.ModelHashes.ElementsAs(ctx, &oldModelHashes, false)
	}

	// Calculate new hashes
	newLambdaHashes := r.calculateLambdaHashes(ctx, newLambdas, routes, lambdaConfig, config)
	newGatewayHashes, _ := calculateAllGatewayHashes(routes, config.FrontendUrls)

	// Calculate new model hashes for change detection
	newModelHashes := make(map[string]string)
	for _, gateway := range newGateways {
		gatewayModels := GetModelsForGateway(routes, models, gateway)
		if h, err := CalculateModelHash(gatewayModels); err == nil && h != "" {
			newModelHashes[gateway] = h
		}
	}

	// Determine what changed FIRST — we need this to decide which IAM reconciliation to run
	var oldSourceHashes map[string]string
	if !state.LambdaSourceHashes.IsNull() && !state.LambdaSourceHashes.IsUnknown() {
		oldSourceHashes = make(map[string]string)
		state.LambdaSourceHashes.ElementsAs(ctx, &oldSourceHashes, false)
	}
	var oldConfigHashes map[string]string
	if !state.LambdaConfigHashes.IsNull() && !state.LambdaConfigHashes.IsUnknown() {
		oldConfigHashes = make(map[string]string)
		state.LambdaConfigHashes.ElementsAs(ctx, &oldConfigHashes, false)
	}

	lambdasToCreate, lambdaUpdateTasks, lambdasToDelete := r.detectLambdaUpdateTasks(ctx, newLambdas, oldLambdaARNs, oldLambdaHashes, newLambdaHashes, oldSourceHashes, oldConfigHashes, routes, lambdaConfig, config)

	// RECONCILE SHARED IAM POLICIES — only when lambdas are being deleted.
	// The purpose of early reconciliation is to detach shared policies from roles
	// BEFORE Terraform deletes those policies concurrently. When no lambdas are
	// being deleted, the post-update reconciliation handles everything.
	if len(lambdasToDelete) > 0 {
		// Only reconcile roles for lambdas being deleted
		utils.Info(ctx, "Reconciling shared IAM policies for lambdas being deleted", map[string]interface{}{
			"lambda_count":         len(lambdasToDelete),
			"desired_policy_count": len(config.SharedIamPolicyArns),
		})

		var reconcileWg sync.WaitGroup
		reconcileConcurrency := config.DockerBuildConcurrency
		if reconcileConcurrency <= 0 {
			reconcileConcurrency = runtime.NumCPU()
		}
		reconcileSem := make(chan struct{}, reconcileConcurrency)
		for _, lambdaName := range lambdasToDelete {
			reconcileWg.Add(1)
			go func(ln string) {
				defer reconcileWg.Done()
				reconcileSem <- struct{}{}
				defer func() { <-reconcileSem }()
				roleName := fmt.Sprintf("%s-%s-%s-lambda-role", config.AppName, config.Environment, ln)
				if err := r.iamManager.ReconcileSharedIamPolicies(ctx, roleName); err != nil {
					utils.Warn(ctx, "Failed to reconcile shared IAM policies for role", map[string]interface{}{
						"lambda": ln,
						"error":  err.Error(),
					})
				}
			}(lambdaName)
		}
		reconcileWg.Wait()

		// Orphaned role scan — only needed when deleting lambdas
		allExistingLambdas := make([]string, 0, len(oldLambdaARNs))
		for lambdaName := range oldLambdaARNs {
			allExistingLambdas = append(allExistingLambdas, lambdaName)
		}
		knownRoles := make(map[string]bool, len(allExistingLambdas))
		for _, lambdaName := range allExistingLambdas {
			roleName := fmt.Sprintf("%s-%s-%s-lambda-role", config.AppName, config.Environment, lambdaName)
			knownRoles[roleName] = true
		}
		orphanedCount, err := r.iamManager.ReconcileOrphanedRoles(ctx, knownRoles)
		if err != nil {
			utils.Warn(ctx, "Failed to fully reconcile orphaned roles", map[string]interface{}{
				"error":          err.Error(),
				"orphaned_found": orphanedCount,
			})
		} else if orphanedCount > 0 {
			utils.Info(ctx, "Reconciled shared policies on orphaned roles", map[string]interface{}{
				"orphaned_count": orphanedCount,
			})
		}
	}

	// Extract lambda names from update tasks for package building
	lambdasToUpdate := make([]string, len(lambdaUpdateTasks))
	for i, task := range lambdaUpdateTasks {
		lambdasToUpdate[i] = task.Action
	}

	utils.Info(ctx, "Detected Lambda changes", map[string]interface{}{
		"to_create": lambdasToCreate,
		"to_update": lambdasToUpdate,
		"to_delete": lambdasToDelete,
	})

	// Build packages for new lambdas and lambdas that need source updates
	lambdasNeedingPackages := make([]string, 0, len(lambdasToCreate)+len(lambdaUpdateTasks))
	lambdasNeedingPackages = append(lambdasNeedingPackages, lambdasToCreate...)
	for _, task := range lambdaUpdateTasks {
		// Only build packages for source or both updates
		if task.UpdateType == LambdaUpdateTypeSource || task.UpdateType == LambdaUpdateTypeBoth {
			lambdasNeedingPackages = append(lambdasNeedingPackages, task.Action)
		}
	}

	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	var buildResults map[string]*BuildResult
	if len(lambdasNeedingPackages) > 0 {
		packageBuilder := NewPackageBuilder(
			config.LambdaSourceDir,
			WithSharedDirs(sharedDirs),
			WithGemDirs(config.LambdaGemDirs),
			WithConcurrency(config.DockerBuildConcurrency),
			WithConfig(config),
		)

		buildResults = packageBuilder.BuildPackages(ctx, lambdasNeedingPackages)

		if packageBuilder.HasFailures(buildResults) {
			resp.Diagnostics.AddError(
				"Failed to build Lambda packages",
				packageBuilder.GetFailureErrors(buildResults).Error(),
			)
			return
		}
	}

	// Initialize parallel manager
	parallelManager := NewParallelManager(config.DockerBuildConcurrency, r.clients, config)

	// Create new Lambdas
	lambdaARNs := make(map[string]string)
	for k, v := range oldLambdaARNs {
		lambdaARNs[k] = v
	}

	if len(lambdasToCreate) > 0 {
		createResults := parallelManager.CreateLambdasInParallel(ctx, lambdasToCreate, buildResults, routes, lambdaConfig, r.iamManager, r.cloudWatchManager)
		for _, result := range createResults {
			if result.Error != nil {
				resp.Diagnostics.AddError(
					"Failed to create Lambda function",
					fmt.Sprintf("Failed to create %s: %s", result.Action, result.Error.Error()),
				)
			} else {
				lambdaARNs[result.Action] = result.ARN
			}
		}
	}

	// Update existing Lambdas
	if len(lambdaUpdateTasks) > 0 {
		updateResults := parallelManager.UpdateLambdasInParallel(ctx, lambdaUpdateTasks, buildResults, routes, lambdaConfig, r.iamManager, r.cloudWatchManager)
		for _, result := range updateResults {
			if result.Error != nil {
				resp.Diagnostics.AddError(
					"Failed to update Lambda function",
					fmt.Sprintf("Failed to update %s: %s", result.Action, result.Error.Error()),
				)
			}
		}
	}

	// Delete removed Lambdas
	if len(lambdasToDelete) > 0 {
		deleteResults := parallelManager.DeleteLambdasInParallel(ctx, lambdasToDelete, r.iamManager, r.cloudWatchManager)
		for _, result := range deleteResults {
			if result.Error != nil {
				resp.Diagnostics.AddWarning(
					"Failed to delete Lambda function",
					fmt.Sprintf("Failed to delete %s: %s", result.Action, result.Error.Error()),
				)
			} else {
				delete(lambdaARNs, result.Action)
			}
		}
	}

	// Reconcile IAM policies for lambdas that were updated or have config changes.
	// Newly created lambdas already had their IAM set up during CreateLambdasInParallel.
	// The early detach pass above already handled removing stale shared policies.
	// Runs in parallel to reduce wall-clock time.
	{
		// Build sets for fast lookup
		newLambdaSet := make(map[string]bool, len(lambdasToCreate))
		for _, created := range lambdasToCreate {
			newLambdaSet[created] = true
		}
		// Only reconcile lambdas whose config changed — source-only updates
		// don't affect IAM policies, so skip them entirely
		configChangedSet := make(map[string]bool, len(lambdaUpdateTasks))
		for _, task := range lambdaUpdateTasks {
			if task.UpdateType == LambdaUpdateTypeConfig || task.UpdateType == LambdaUpdateTypeBoth {
				configChangedSet[task.Action] = true
			}
		}

		lambdasToReconcile := make([]string, 0)
		for _, lambdaName := range newLambdas {
			if newLambdaSet[lambdaName] {
				continue // Already handled during create
			}
			if configChangedSet[lambdaName] {
				lambdasToReconcile = append(lambdasToReconcile, lambdaName)
				continue
			}
			// Also reconcile lambdas with sqs_triggers that may be missing their
			// SQS IAM policy (e.g., deployed before SQS policy support was added).
			// PutRolePolicy is idempotent so this is safe to run on every apply.
			if len(extractSQSTriggers(lambdaConfig, lambdaName)) > 0 {
				lambdasToReconcile = append(lambdasToReconcile, lambdaName)
			}
		}

		utils.Info(ctx, "Reconciling IAM policies for updated lambdas (parallel)", map[string]interface{}{
			"reconcile_count":     len(lambdasToReconcile),
			"total_lambdas":       len(newLambdas),
			"shared_policy_count": len(config.SharedIamPolicyArns),
		})

		var iamWg sync.WaitGroup
		var iamErrMu sync.Mutex
		var iamErrors []string
		iamConcurrency := config.DockerBuildConcurrency
		if iamConcurrency <= 0 {
			iamConcurrency = runtime.NumCPU()
		}
		iamSem := make(chan struct{}, iamConcurrency)
		for _, lambdaName := range lambdasToReconcile {
			iamWg.Add(1)
			go func(ln string) {
				defer iamWg.Done()
				iamSem <- struct{}{}
				defer func() { <-iamSem }()

				roleName := fmt.Sprintf("%s-%s-%s-lambda-role", config.AppName, config.Environment, ln)
				lambdaRoutes := filterRoutesByLambda(routes, ln)

				// Detach stale policies first to stay within the 10-policy-per-role AWS limit
				if err := r.iamManager.ReconcileSharedIamPolicies(ctx, roleName); err != nil {
					utils.Warn(ctx, "Failed to reconcile shared IAM policies before attach", map[string]interface{}{
						"lambda": ln,
						"error":  err.Error(),
					})
				}

				if err := r.iamManager.AttachSharedIamPolicies(ctx, roleName); err != nil {
					iamErrMu.Lock()
					iamErrors = append(iamErrors, fmt.Sprintf("lambda %s: %s", ln, err.Error()))
					iamErrMu.Unlock()
					return
				}
				if err := r.iamManager.CreateDynamoDBPoliciesForAction(ctx, ln, lambdaRoutes, roleName); err != nil {
					utils.Warn(ctx, "Failed to reconcile DynamoDB policies", map[string]interface{}{
						"lambda": ln,
						"error":  err.Error(),
					})
				}
				if err := r.iamManager.CreateS3PoliciesForAction(ctx, ln, roleName); err != nil {
					utils.Warn(ctx, "Failed to reconcile S3 policies", map[string]interface{}{
						"lambda": ln,
						"error":  err.Error(),
					})
				}
				if err := r.iamManager.CreateSESPoliciesForAction(ctx, ln, roleName); err != nil {
					utils.Warn(ctx, "Failed to reconcile SES policies", map[string]interface{}{
						"lambda": ln,
						"error":  err.Error(),
					})
				}
				if err := r.iamManager.CreateSQSPoliciesForAction(ctx, ln, roleName); err != nil {
					utils.Warn(ctx, "Failed to reconcile SQS policies", map[string]interface{}{
						"lambda": ln,
						"error":  err.Error(),
					})
				}
			}(lambdaName)
		}
		iamWg.Wait()

		if len(iamErrors) > 0 {
			resp.Diagnostics.AddError(
				"Failed to attach shared IAM policies",
				strings.Join(iamErrors, "; "),
			)
		}
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Handle Gateway changes
	gatewaysToCreate, gatewaysToUpdate, gatewaysToDelete := r.detectGatewayChanges(newGateways, oldGatewayIDs, oldGatewayHashes, newGatewayHashes, oldModelHashes, newModelHashes)

	utils.Info(ctx, "Detected Gateway changes", map[string]interface{}{
		"to_create":          gatewaysToCreate,
		"to_update":          gatewaysToUpdate,
		"to_delete":          gatewaysToDelete,
		"old_gateway_hashes": oldGatewayHashes,
		"new_gateway_hashes": newGatewayHashes,
	})

	gatewayIDs := make(map[string]string)
	gatewayURLs := make(map[string]string)
	for k, v := range oldGatewayIDs {
		gatewayIDs[k] = v
	}

	var oldGatewayURLs map[string]string
	if !state.ApiGatewayUrls.IsNull() && !state.ApiGatewayUrls.IsUnknown() {
		oldGatewayURLs = make(map[string]string)
		state.ApiGatewayUrls.ElementsAs(ctx, &oldGatewayURLs, false)
	}
	for k, v := range oldGatewayURLs {
		gatewayURLs[k] = v
	}

	// Create new Gateways
	if len(gatewaysToCreate) > 0 {
		createResults := parallelManager.CreateGatewaysInParallel(ctx, gatewaysToCreate, routes, lambdaARNs, config, models)
		for _, result := range createResults {
			if result.Error != nil {
				resp.Diagnostics.AddError(
					"Failed to create API Gateway",
					fmt.Sprintf("Failed to create %s: %s", result.Gateway, result.Error.Error()),
				)
			} else {
				gatewayIDs[result.Gateway] = result.ID
				gatewayURLs[result.Gateway] = result.URL
			}
		}
	}

	// Update existing Gateways (routes changed)
	if len(gatewaysToUpdate) > 0 {
		updateResults := parallelManager.UpdateGatewaysInParallel(ctx, gatewaysToUpdate, routes, lambdaARNs, config, models)
		for _, result := range updateResults {
			if result.Error != nil {
				resp.Diagnostics.AddWarning(
					"Failed to update API Gateway",
					fmt.Sprintf("Failed to update %s: %s", result.Gateway, result.Error.Error()),
				)
			}
		}
	}

	// Delete base path mappings for gateways being deleted BEFORE deleting the gateways.
	// AWS rejects DeleteRestApi if base path mappings still reference the API.
	if len(gatewaysToDelete) > 0 {
		// Try plan's custom domain first, fall back to state's custom domain
		customDomainForCleanup := ""
		if !plan.CustomDomainName.IsNull() && !plan.CustomDomainName.IsUnknown() {
			customDomainForCleanup = plan.CustomDomainName.ValueString()
		}
		if customDomainForCleanup == "" && !state.CustomDomainName.IsNull() && !state.CustomDomainName.IsUnknown() {
			customDomainForCleanup = state.CustomDomainName.ValueString()
		}
		if customDomainForCleanup != "" {
			for _, gateway := range gatewaysToDelete {
				utils.Info(ctx, "Deleting base path mapping before gateway deletion", map[string]interface{}{
					"gateway":     gateway,
					"domain_name": customDomainForCleanup,
				})
				if err := r.basePathMappingManager.DeleteBasePathMapping(ctx, customDomainForCleanup, gateway); err != nil {
					resp.Diagnostics.AddWarning(
						"Base Path Mapping Deletion Warning",
						fmt.Sprintf("Failed to delete base path mapping '/%s' before gateway deletion: %s", gateway, err.Error()),
					)
				}
			}
		}
	}

	// Delete removed Gateways
	if len(gatewaysToDelete) > 0 {
		deleteResults := parallelManager.DeleteGatewaysInParallel(ctx, gatewaysToDelete, r.cloudWatchManager)
		for _, result := range deleteResults {
			if result.Error != nil {
				resp.Diagnostics.AddWarning(
					"Failed to delete API Gateway",
					fmt.Sprintf("Failed to delete %s: %s", result.Gateway, result.Error.Error()),
				)
			} else {
				delete(gatewayIDs, result.Gateway)
				delete(gatewayURLs, result.Gateway)
			}
		}
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Sync base path mappings if custom_domain_name is provided
	basePathMappings := make(map[string]string)
	customDomainUrl := ""

	// Get old base path mappings from state
	var oldBasePathMappings map[string]string
	if !state.BasePathMappings.IsNull() && !state.BasePathMappings.IsUnknown() {
		oldBasePathMappings = make(map[string]string)
		state.BasePathMappings.ElementsAs(ctx, &oldBasePathMappings, false)
	}

	if !plan.CustomDomainName.IsNull() && !plan.CustomDomainName.IsUnknown() {
		customDomainName := plan.CustomDomainName.ValueString()
		if customDomainName != "" {
			// Build custom domain URL (always needed for state)
			customDomainUrl = r.basePathMappingManager.BuildCustomDomainURL(customDomainName)

			// Only do the full sync when gateways were added or removed.
			// When only routes changed within existing gateways, mappings are unchanged.
			gatewayStructureChanged := len(gatewaysToCreate) > 0 || len(gatewaysToDelete) > 0
			if gatewayStructureChanged {
				utils.Info(ctx, "Syncing base path mappings for custom domain", map[string]interface{}{
					"custom_domain_name": customDomainName,
					"gateway_count":      len(gatewayIDs),
					"gateways_created":   gatewaysToCreate,
					"gateways_deleted":   gatewaysToDelete,
				})

				// Validate custom domain exists
				if err := r.basePathMappingManager.ValidateCustomDomainExists(ctx, customDomainName); err != nil {
					resp.Diagnostics.AddError(
						"Custom Domain Validation Failed",
						err.Error(),
					)
					return
				}

				// Sync base path mappings - this handles create, update, and delete
				stageName := config.Environment
				mappings, err := r.basePathMappingManager.SyncBasePathMappings(ctx, customDomainName, gatewayIDs, stageName)
				if err != nil {
					resp.Diagnostics.AddWarning(
						"Base Path Mapping Warning",
						fmt.Sprintf("Some base path mappings could not be synced: %s", err.Error()),
					)
				}

				// Store successful mappings
				for gateway := range gatewayIDs {
					basePathMappings[gateway] = gateway
				}

				// Update mappings with actual results
				if mappings != nil {
					for basePath := range mappings {
						basePathMappings[basePath] = basePath
					}
				}
			} else {
				// No gateway structure change — carry forward existing mappings
				for gateway := range gatewayIDs {
					basePathMappings[gateway] = gateway
				}
			}

			utils.Info(ctx, "Base path mappings resolved", map[string]interface{}{
				"custom_domain_url":       customDomainUrl,
				"mapping_count":           len(basePathMappings),
				"gateway_structure_changed": gatewayStructureChanged,
			})
		}
	} else {
		// Custom domain was removed or not set - delete any existing mappings
		if len(oldBasePathMappings) > 0 && !state.CustomDomainName.IsNull() && !state.CustomDomainName.IsUnknown() {
			oldCustomDomainName := state.CustomDomainName.ValueString()
			if oldCustomDomainName != "" {
				utils.Info(ctx, "Custom domain removed, deleting base path mappings", map[string]interface{}{
					"old_custom_domain_name": oldCustomDomainName,
					"mapping_count":          len(oldBasePathMappings),
				})

				// Delete all old mappings
				for basePath := range oldBasePathMappings {
					if err := r.basePathMappingManager.DeleteBasePathMapping(ctx, oldCustomDomainName, basePath); err != nil {
						resp.Diagnostics.AddWarning(
							"Base Path Mapping Deletion Warning",
							fmt.Sprintf("Failed to delete base path mapping '/%s': %s", basePath, err.Error()),
						)
					}
				}
			}
		}
	}

	// Calculate final hashes
	routesHash, _ := calculateConfigHash(routes, models, lambdaConfig, config.ReadOnlyTables, config.ReadWriteTables)
	gatewayHashes, _ := calculateAllGatewayHashes(routes, config.FrontendUrls)

	// Update state
	plan.ID = state.ID

	lambdaFunctionsMap, diags := types.MapValueFrom(ctx, types.StringType, lambdaARNs)
	resp.Diagnostics.Append(diags...)
	plan.LambdaFunctions = lambdaFunctionsMap

	gatewayIDsMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayIDs)
	resp.Diagnostics.Append(diags...)
	plan.ApiGatewayIds = gatewayIDsMap

	gatewayURLsMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayURLs)
	resp.Diagnostics.Append(diags...)
	plan.ApiGatewayUrls = gatewayURLsMap

	plan.RoutesHash = types.StringValue(routesHash)

	// Store routes JSON for change detection
	routesJson, err := json.Marshal(routes)
	if err != nil {
		resp.Diagnostics.AddError("Failed to serialize routes", err.Error())
		return
	}
	plan.RoutesJson = types.StringValue(string(routesJson))

	lambdaHashesMap, diags := types.MapValueFrom(ctx, types.StringType, newLambdaHashes)
	resp.Diagnostics.Append(diags...)
	plan.LambdaHashes = lambdaHashesMap

	// Store separate source/config hashes for precise change detection
	newSourceHashes, newConfigHashes := r.calculateSeparateLambdaHashes(ctx, newLambdas, routes, lambdaConfig, config)
	newSourceHashesMap, diags := types.MapValueFrom(ctx, types.StringType, newSourceHashes)
	resp.Diagnostics.Append(diags...)
	plan.LambdaSourceHashes = newSourceHashesMap
	newConfigHashesMap, diags := types.MapValueFrom(ctx, types.StringType, newConfigHashes)
	resp.Diagnostics.Append(diags...)
	plan.LambdaConfigHashes = newConfigHashesMap

	gatewayHashesMap, diags := types.MapValueFrom(ctx, types.StringType, gatewayHashes)
	resp.Diagnostics.Append(diags...)
	plan.GatewayHashes = gatewayHashesMap

	// Calculate and store model hashes (only if plan marked them as unknown)
	if plan.ModelHashes.IsUnknown() {
		updateModelHashesMap := make(map[string]string)
		for _, gateway := range newGateways {
			gatewayModels := GetModelsForGateway(routes, models, gateway)
			if h, err := CalculateModelHash(gatewayModels); err == nil && h != "" {
				updateModelHashesMap[gateway] = h
			}
		}
		modelHashesTfMap, diags := types.MapValueFrom(ctx, types.StringType, updateModelHashesMap)
		resp.Diagnostics.Append(diags...)
		plan.ModelHashes = modelHashesTfMap
	}

	// Generate OpenAPI specs as read-only side effect (Phase 1)
	// Always run the side effect to write spec files to disk, but only update
	// the hash in state for gateways that the plan marked as unknown.
	// This prevents "inconsistent result after apply" errors when only lambda source changes.
	openAPISpecHashes := r.generateOpenAPISpecsSideEffect(ctx, routes, lambdaARNs, models, config)


	if !plan.OpenAPISpecHashes.IsNull() {
		// Merge: replace unknown elements with computed values, keep known elements
		planElements := plan.OpenAPISpecHashes.Elements()
		mergedElements := make(map[string]attr.Value)
		for k, v := range planElements {
			if v.IsUnknown() {
				if computed, ok := openAPISpecHashes[k]; ok {
					mergedElements[k] = types.StringValue(computed)
				}
			} else {
				mergedElements[k] = v
			}
		}
		mergedMap, diags := types.MapValue(types.StringType, mergedElements)
		resp.Diagnostics.Append(diags...)
		plan.OpenAPISpecHashes = mergedMap
	}

	// Set base path mappings and custom domain URL
	basePathMappingsMap, diags := types.MapValueFrom(ctx, types.StringType, basePathMappings)
	resp.Diagnostics.Append(diags...)
	plan.BasePathMappings = basePathMappingsMap

	if customDomainUrl != "" {
		plan.CustomDomainUrl = types.StringValue(customDomainUrl)
	} else {
		plan.CustomDomainUrl = types.StringNull()
	}

	utils.Info(ctx, "Successfully updated dispatcher resource", map[string]interface{}{
		"id": plan.ID.ValueString(),
	})

	// Set plan-time known attributes from parsed routes
	gatewayNamesList, diags := types.ListValueFrom(ctx, types.StringType, newGateways)
	resp.Diagnostics.Append(diags...)
	plan.GatewayNames = gatewayNamesList

	lambdaNamesList, diags := types.ListValueFrom(ctx, types.StringType, newLambdas)
	resp.Diagnostics.Append(diags...)
	plan.LambdaNames = lambdaNamesList

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// detectLambdaChanges determines which lambdas need to be created, updated, or deleted
func (r *dispatcherResource) detectLambdaChanges(newLambdas []string, oldARNs map[string]string, oldHashes, newHashes map[string]string) (toCreate, toUpdate, toDelete []string) {
	newSet := make(map[string]bool)
	for _, l := range newLambdas {
		newSet[l] = true
	}

	// Find lambdas to create (in new but not in old)
	for _, l := range newLambdas {
		if _, exists := oldARNs[l]; !exists {
			toCreate = append(toCreate, l)
		}
	}

	// Find lambdas to update (in both but hash changed)
	for _, l := range newLambdas {
		if _, exists := oldARNs[l]; exists {
			oldHash := oldHashes[l]
			newHash := newHashes[l]
			if oldHash != newHash {
				toUpdate = append(toUpdate, l)
			}
		}
	}

	// Find lambdas to delete (in old but not in new)
	for l := range oldARNs {
		if !newSet[l] {
			toDelete = append(toDelete, l)
		}
	}

	return toCreate, toUpdate, toDelete
}

// detectGatewayChanges determines which gateways need to be created, updated, or deleted
// Uses gateway hashes to detect route changes and model hashes to detect schema changes
func (r *dispatcherResource) detectGatewayChanges(newGateways []string, oldIDs map[string]string, oldGatewayHashes, newGatewayHashes, oldModelHashes, newModelHashes map[string]string) (toCreate, toUpdate, toDelete []string) {
	newSet := make(map[string]bool)
	for _, g := range newGateways {
		newSet[g] = true
	}

	// Find gateways to create (in new but not in old)
	for _, g := range newGateways {
		if _, exists := oldIDs[g]; !exists {
			toCreate = append(toCreate, g)
		}
	}

	// Find gateways to update (in both but gateway hash or model hash changed)
	for _, g := range newGateways {
		if _, exists := oldIDs[g]; exists {
			gatewayChanged := oldGatewayHashes[g] != newGatewayHashes[g]
			modelChanged := oldModelHashes[g] != newModelHashes[g]
			if gatewayChanged || modelChanged {
				toUpdate = append(toUpdate, g)
			}
		}
	}

	// Find gateways to delete (in old but not in new)
	for g := range oldIDs {
		if !newSet[g] {
			toDelete = append(toDelete, g)
		}
	}

	return toCreate, toUpdate, toDelete
}


// detectLambdaUpdateTasks determines which lambdas need updates and what type of update
// Returns update tasks with the appropriate update type (source, config, or both)
func (r *dispatcherResource) detectLambdaUpdateTasks(
	ctx context.Context,
	newLambdas []string,
	oldARNs map[string]string,
	oldLambdaHashes, newLambdaHashes map[string]string,
	oldSourceHashes, oldConfigHashes map[string]string,
	routes []utils.Route,
	lambdaConfig map[string]interface{},
	config *DispatcherConfig,
) (toCreate []string, updateTasks []LambdaUpdateTask, toDelete []string) {
	newSet := make(map[string]bool)
	for _, l := range newLambdas {
		newSet[l] = true
	}

	// Find lambdas to create (in new but not in old)
	for _, l := range newLambdas {
		if _, exists := oldARNs[l]; !exists {
			toCreate = append(toCreate, l)
		}
	}

	// Find lambdas to update (in both but hash changed)
	// Determine update type by comparing source and config hashes separately
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	// Pre-filter routes by lambda for consistent hash computation
	routeLambdaGroups := utils.GroupByLambda(routes)

	for _, l := range newLambdas {
		if _, exists := oldARNs[l]; exists {
			oldHash := oldLambdaHashes[l]
			newHash := newLambdaHashes[l]
			if oldHash != newHash {
				updateType := LambdaUpdateTypeBoth

				// If we have separate source/config hashes from state, determine precise update type
				if oldSourceHashes != nil && oldConfigHashes != nil {
					newSourceHash, sourceErr := calculateLambdaSourceHash(config.LambdaSourceDir, l, sharedDirs, config.LambdaGemDirs...)
					lambdaRoutes := routeLambdaGroups[l]
					newConfigHash, configErr := calculateLambdaConfigHash(lambdaRoutes, lambdaConfig, l, config.LambdaLayerArns, config.AlarmConfig, config.ReadOnlyTables, config.ReadWriteTables, config.SharedIamPolicyArns)

					if sourceErr == nil && configErr == nil {
						sourceChanged := oldSourceHashes[l] != newSourceHash
						configChanged := oldConfigHashes[l] != newConfigHash

						if sourceChanged && configChanged {
							updateType = LambdaUpdateTypeBoth
						} else if sourceChanged {
							updateType = LambdaUpdateTypeSource
						} else if configChanged {
							updateType = LambdaUpdateTypeConfig
						}

						utils.Info(ctx, "Lambda hash changed, determined update type", map[string]interface{}{
							"lambda":         l,
							"update_type":    string(updateType),
							"source_changed": sourceChanged,
							"config_changed": configChanged,
						})
					}
				}

				updateTasks = append(updateTasks, LambdaUpdateTask{
					Action:     l,
					UpdateType: updateType,
					ARN:        oldARNs[l],
				})
			}
		}
	}

	// Find lambdas to delete (in old but not in new)
	for l := range oldARNs {
		if !newSet[l] {
			toDelete = append(toDelete, l)
		}
	}

	return toCreate, updateTasks, toDelete
}

// generateOpenAPISpecsSideEffect generates OpenAPI specs for all gateways as a
// read-only side effect (Phase 1). It logs the specs at DEBUG level and returns
// a map of gateway name → spec hash for storage in state. Errors are logged as
// warnings and do not block the apply.
func (r *dispatcherResource) generateOpenAPISpecsSideEffect(
	ctx context.Context,
	routes []utils.Route,
	lambdaARNs map[string]string,
	models []utils.ModelDefinition,
	config *DispatcherConfig,
) map[string]string {
	gen := NewOpenAPIGenerator(config)
	specs, err := gen.GenerateAllSpecs(ctx, routes, lambdaARNs, models)
	if err != nil {
		utils.Warn(ctx, "OpenAPI spec generation failed (Phase 1 side effect)", map[string]interface{}{
			"error": err.Error(),
		})
		return map[string]string{}
	}

	// Write specs to {project_root}/.conveyor-belt/openapi/ for inspection
	// Project root is the parent of lambda_source_dir (e.g., lambda_source_dir="/path/to/project/lambda" → project root="/path/to/project")
	projectRoot := filepath.Dir(config.LambdaSourceDir)
	specDir := filepath.Join(projectRoot, ".conveyor-belt", "openapi")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		utils.Warn(ctx, "Failed to create OpenAPI spec output directory", map[string]interface{}{
			"dir": specDir, "error": err.Error(),
		})
	}

	hashes := make(map[string]string)
	for gw, spec := range specs {
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			utils.Warn(ctx, "Failed to marshal OpenAPI spec", map[string]interface{}{
				"gateway": gw, "error": err.Error(),
			})
			continue
		}

		// Write spec file to disk
		specFile := filepath.Join(specDir, gw+".json")
		if writeErr := os.WriteFile(specFile, data, 0644); writeErr != nil {
			utils.Warn(ctx, "Failed to write OpenAPI spec file", map[string]interface{}{
				"file": specFile, "error": writeErr.Error(),
			})
		} else {
			utils.Info(ctx, "Wrote OpenAPI spec to disk", map[string]interface{}{
				"file":    specFile,
				"gateway": gw,
				"size":    len(data),
			})
		}

		hash, err := gen.CalculateSpecHash(ctx, gw, routes, lambdaARNs, models)
		if err != nil {
			utils.Warn(ctx, "Failed to calculate OpenAPI spec hash", map[string]interface{}{
				"gateway": gw, "error": err.Error(),
			})
			continue
		}
		hashes[gw] = hash
	}

	utils.Info(ctx, "OpenAPI specs generated (Phase 1 side effect)", map[string]interface{}{
		"gateway_count": len(hashes),
		"output_dir":    specDir,
	})

	return hashes
}

// generateSpecsDuringPlan generates OpenAPI specs as a
// read-only side effect during terraform plan. This lets agents and tools
// inspect the generated specs without running a full deploy.
// Uses Lambda ARNs from state if available, otherwise the generator constructs
// them from the naming convention.
func (r *dispatcherResource) generateSpecsDuringPlan(
	ctx context.Context,
	plan *DispatcherResourceModel,
	state *DispatcherResourceModel,
	routes []utils.Route,
	models []utils.ModelDefinition,
) {
	lambdaSourceDir := plan.LambdaSourceDir.ValueString()
	if lambdaSourceDir == "" {
		return
	}

	// Build minimal config for the generator
	config := &DispatcherConfig{
		AppName:     plan.AppName.ValueString(),
		Environment: r.providerConfig.Environment,
		AwsRegion:   r.providerConfig.AwsRegion,
	}
	if !plan.FrontendUrls.IsNull() && !plan.FrontendUrls.IsUnknown() {
		plan.FrontendUrls.ElementsAs(ctx, &config.FrontendUrls, false)
	}
	if !plan.CognitoUserPoolArns.IsNull() && !plan.CognitoUserPoolArns.IsUnknown() {
		plan.CognitoUserPoolArns.ElementsAs(ctx, &config.CognitoUserPoolArns, false)
	}
	if !plan.FriendlyErrors.IsNull() && !plan.FriendlyErrors.IsUnknown() {
		config.FriendlyErrors = plan.FriendlyErrors.ValueBool()
	}

	// Get Lambda ARNs from state (if resource already exists)
	lambdaARNs := make(map[string]string)
	if !state.LambdaFunctions.IsNull() && !state.LambdaFunctions.IsUnknown() {
		state.LambdaFunctions.ElementsAs(ctx, &lambdaARNs, false)
	}

	// Get account ID from existing ARNs if possible, otherwise fetch it
	if config.AwsAccountId == "" {
		for _, arn := range lambdaARNs {
			parts := strings.SplitN(arn, ":", 6)
			if len(parts) >= 5 {
				config.AwsAccountId = parts[4]
				break
			}
		}
	}
	if config.AwsAccountId == "" {
		if id, err := getAwsAccountId(ctx, config.AwsRegion); err == nil {
			config.AwsAccountId = id
		}
	}

	gen := NewOpenAPIGenerator(config)
	specs, err := gen.GenerateAllSpecs(ctx, routes, lambdaARNs, models)
	if err != nil {
		tflog.Debug(ctx, "[CONVEYOR-BELT_PLAN] OpenAPI spec generation failed (non-blocking)", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	projectRoot := filepath.Dir(lambdaSourceDir)
	specDir := filepath.Join(projectRoot, ".conveyor-belt", "openapi")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		return
	}

	for gw, spec := range specs {
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			continue
		}
		os.WriteFile(filepath.Join(specDir, gw+".json"), data, 0644)
	}

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Wrote OpenAPI specs to .conveyor-belt/", map[string]interface{}{
		"output_dir":    specDir,
		"gateway_count": len(specs),
	})
}

// Delete deletes the resource and removes the Terraform state.
func (r *dispatcherResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DispatcherResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if r.client != nil {
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

	utils.Info(ctx, "Deleting dispatcher resource", map[string]interface{}{
		"id": state.ID.ValueString(),
	})

	// Get current lambdas and gateways from state
	var lambdaARNs map[string]string
	if !state.LambdaFunctions.IsNull() && !state.LambdaFunctions.IsUnknown() {
		lambdaARNs = make(map[string]string)
		state.LambdaFunctions.ElementsAs(ctx, &lambdaARNs, false)
	}

	var gatewayIDs map[string]string
	if !state.ApiGatewayIds.IsNull() && !state.ApiGatewayIds.IsUnknown() {
		gatewayIDs = make(map[string]string)
		state.ApiGatewayIds.ElementsAs(ctx, &gatewayIDs, false)
	}

	// Initialize parallel manager
	parallelManager := NewParallelManager(config.DockerBuildConcurrency, r.clients, config)

	// Delete base path mappings first (before deleting API Gateways)
	if !state.CustomDomainName.IsNull() && !state.CustomDomainName.IsUnknown() {
		customDomainName := state.CustomDomainName.ValueString()
		if customDomainName != "" {
			var basePathMappings map[string]string
			if !state.BasePathMappings.IsNull() && !state.BasePathMappings.IsUnknown() {
				basePathMappings = make(map[string]string)
				state.BasePathMappings.ElementsAs(ctx, &basePathMappings, false)
			}

			if len(basePathMappings) > 0 {
				utils.Info(ctx, "Deleting base path mappings", map[string]interface{}{
					"custom_domain_name": customDomainName,
					"mapping_count":      len(basePathMappings),
				})

				for basePath := range basePathMappings {
					if err := r.basePathMappingManager.DeleteBasePathMapping(ctx, customDomainName, basePath); err != nil {
						resp.Diagnostics.AddWarning(
							"Failed to delete base path mapping",
							fmt.Sprintf("Failed to delete base path mapping '/%s' on domain '%s': %s", basePath, customDomainName, err.Error()),
						)
					}
				}

				utils.Info(ctx, "Deleted base path mappings", map[string]interface{}{
					"custom_domain_name": customDomainName,
				})
			}
		}
	}

	// Delete all API Gateways first (they depend on Lambdas)
	if len(gatewayIDs) > 0 {
		gateways := make([]string, 0, len(gatewayIDs))
		for g := range gatewayIDs {
			gateways = append(gateways, g)
		}

		utils.Info(ctx, "Deleting API Gateways", map[string]interface{}{
			"count": len(gateways),
		})

		deleteResults := parallelManager.DeleteGatewaysInParallel(ctx, gateways, r.cloudWatchManager)
		for _, result := range deleteResults {
			if result.Error != nil {
				resp.Diagnostics.AddWarning(
					"Failed to delete API Gateway",
					fmt.Sprintf("Failed to delete %s: %s", result.Gateway, result.Error.Error()),
				)
			}
		}
	}

	// Delete all Lambda functions
	if len(lambdaARNs) > 0 {
		lambdas := make([]string, 0, len(lambdaARNs))
		for l := range lambdaARNs {
			lambdas = append(lambdas, l)
		}

		utils.Info(ctx, "Deleting Lambda functions", map[string]interface{}{
			"count": len(lambdas),
		})

		deleteResults := parallelManager.DeleteLambdasInParallel(ctx, lambdas, r.iamManager, r.cloudWatchManager)
		for _, result := range deleteResults {
			if result.Error != nil {
				resp.Diagnostics.AddWarning(
					"Failed to delete Lambda function",
					fmt.Sprintf("Failed to delete %s: %s", result.Action, result.Error.Error()),
				)
			}
		}
	}

	utils.Info(ctx, "Successfully deleted dispatcher resource", map[string]interface{}{
		"id": state.ID.ValueString(),
	})
}

// ImportState imports an existing dispatcher resource into Terraform state.
// Import ID format: {app_name}-{environment}
//
// The import process works as follows:
// 1. Parse the import ID to extract app_name and environment
// 2. Set the ID and app_name in state
// 3. The Read method will be called automatically to populate Lambda and Gateway state
//
// After import, the user must provide these required attributes in their Terraform configuration:
// - source: Path to routes.tf.rb file
// - lambda_source_dir: Directory containing Lambda source files
// - frontend_urls: Frontend URLs for CORS configuration
//
// Example usage:
//
//	terraform import dispatcher.main myapp-prod
func (r *dispatcherResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importID := req.ID

	utils.Info(ctx, "Importing dispatcher resource", map[string]interface{}{
		"import_id": importID,
	})

	// Validate import ID format
	if importID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			"Import ID cannot be empty. Expected format: {app_name}-{environment}",
		)
		return
	}

	// Parse app_name and environment from import ID (format: app_name-environment)
	appName, environment, err := parseImportID(importID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID Format",
			fmt.Sprintf("Could not parse import ID '%s': %s. Expected format: {app_name}-{environment}", importID, err.Error()),
		)
		return
	}

	utils.Info(ctx, "Parsed import ID", map[string]interface{}{
		"app_name":    appName,
		"environment": environment,
	})

	// Set the ID attribute - this is the primary identifier
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), importID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Set app_name - this is required for the Read method to work
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_name"), appName)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Initialize empty computed maps to prevent nil pointer issues during Read
	// These will be populated by the Read method after import
	emptyStringMap := map[string]string{}

	lambdaFunctionsMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("lambda_functions"), lambdaFunctionsMap)...)

	gatewayIDsMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_gateway_ids"), gatewayIDsMap)...)

	gatewayURLsMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_gateway_urls"), gatewayURLsMap)...)

	lambdaHashesMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("lambda_hashes"), lambdaHashesMap)...)

	lambdaSourceHashesMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("lambda_source_hashes"), lambdaSourceHashesMap)...)

	lambdaConfigHashesMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("lambda_config_hashes"), lambdaConfigHashesMap)...)

	gatewayHashesMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_hashes"), gatewayHashesMap)...)

	// Set empty model_hashes
	modelHashesMap, diags := types.MapValueFrom(ctx, types.StringType, emptyStringMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("model_hashes"), modelHashesMap)...)

	// Set empty gateway_names and lambda_names (will be populated by Read/Plan)
	emptyStringList := []string{}
	gatewayNamesList, diags := types.ListValueFrom(ctx, types.StringType, emptyStringList)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_names"), gatewayNamesList)...)

	lambdaNamesList, diags := types.ListValueFrom(ctx, types.StringType, emptyStringList)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("lambda_names"), lambdaNamesList)...)

	// Set empty routes_hash
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("routes_hash"), "")...)

	utils.Info(ctx, "Successfully set import state attributes", map[string]interface{}{
		"id":       importID,
		"app_name": appName,
	})

	// Provide guidance on required configuration after import
	resp.Diagnostics.AddWarning(
		"Import requires additional configuration",
		fmt.Sprintf(`After import, you must set the following required attributes in your Terraform configuration:

  resource "conveyor_belt" "main" {
    source            = "path/to/routes.tf.rb"  # Required: Path to your routes file
    app_name          = "%s"                     # Already set from import
    lambda_source_dir = "path/to/lambda"         # Required: Lambda source directory
    frontend_urls     = ["https://example.com"]  # Required: Frontend URLs for CORS
    
    # Optional attributes you may want to configure:
    # cognito_user_pool_arns = [...]
    # shared_iam_policy_arns = [...]
    # lambda_layer_arns      = [...]
    # lambda_shared_dirs     = ["models", "lib", "helpers", "templates"]
    # read_only_tables       = [...]
    # read_write_tables      = [...]
    # lambda_config          = { ... }
    # alarm_config           = { ... }
  }

After adding the required configuration, run 'terraform plan' to see the current state of your imported resources.
The Read operation will discover existing Lambda functions and API Gateways based on the naming convention: %s-%s-{resource_name}`,
			appName, appName, environment),
	)
}

// discoverManagedResources queries AWS to find which of the given lambdas and gateways
// already exist. Unlike broad prefix discovery, this only checks specific resources
// that the routes file says should be managed. Returns maps of discovered ARNs and IDs.
func (r *dispatcherResource) discoverManagedResources(ctx context.Context, prefix string, lambdas []string, gateways []string) (lambdaARNs map[string]string, gatewayIDs map[string]string) {
	lambdaARNs = make(map[string]string)
	gatewayIDs = make(map[string]string)

	// Discover Lambda functions by checking each one individually
	for _, name := range lambdas {
		functionName := prefix + name
		getOutput, err := r.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
			FunctionName: aws.String(functionName),
		})
		if err != nil {
			// Not found or error — skip
			continue
		}
		if getOutput.Configuration != nil && getOutput.Configuration.FunctionArn != nil {
			lambdaARNs[name] = *getOutput.Configuration.FunctionArn
		}
	}

	// Discover API Gateways by listing all and filtering
	apisOutput, err := r.clients.ApiGateway.GetRestApis(ctx, &apigateway.GetRestApisInput{
		Limit: aws.Int32(500),
	})
	if err == nil {
		gatewaySet := make(map[string]bool)
		for _, g := range gateways {
			gatewaySet[g] = true
		}
		for _, api := range apisOutput.Items {
			if api.Name != nil && api.Id != nil {
				shortName := strings.TrimPrefix(*api.Name, prefix)
				if gatewaySet[shortName] {
					gatewayIDs[shortName] = *api.Id
				}
			}
		}
	}

	tflog.Info(ctx, "[CONVEYOR-BELT_PLAN] Discovered managed resources from AWS", map[string]interface{}{
		"lambdas_found":  len(lambdaARNs),
		"gateways_found": len(gatewayIDs),
	})

	return lambdaARNs, gatewayIDs
}

// parseImportID parses the import ID into app_name and environment components.
// The import ID format is: {app_name}-{environment}
// For app names containing hyphens, the last hyphen-separated segment is treated as the environment.
func parseImportID(id string) (appName, environment string, err error) {
	if id == "" {
		return "", "", fmt.Errorf("import ID cannot be empty")
	}

	// Find the last hyphen to split app_name and environment
	lastDash := -1
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '-' {
			lastDash = i
			break
		}
	}

	if lastDash < 0 {
		return "", "", fmt.Errorf("import ID must contain at least one hyphen separating app_name and environment")
	}

	appName = id[:lastDash]
	environment = id[lastDash+1:]

	if appName == "" {
		return "", "", fmt.Errorf("app_name cannot be empty")
	}

	if environment == "" {
		return "", "", fmt.Errorf("environment cannot be empty")
	}

	return appName, environment, nil
}

// splitImportID splits the import ID into parts (deprecated, use parseImportID instead)
func splitImportID(id string) []string {
	// Simple split - assumes format app_name-environment
	// For more complex app names with hyphens, user may need to adjust
	parts := make([]string, 0)
	lastDash := -1
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '-' {
			lastDash = i
			break
		}
	}
	if lastDash > 0 {
		parts = append(parts, id[:lastDash])
		parts = append(parts, id[lastDash+1:])
	} else {
		parts = append(parts, id)
	}
	return parts
}

// filterRoutesByGatewayFromState parses routes from stored JSON and filters by gateway
func filterRoutesByGatewayFromState(ctx context.Context, state *DispatcherResourceModel, gateway string) []utils.Route {
	if state.RoutesJson.IsNull() || state.RoutesJson.IsUnknown() {
		return []utils.Route{}
	}

	var allRoutes []utils.Route
	if err := json.Unmarshal([]byte(state.RoutesJson.ValueString()), &allRoutes); err != nil {
		tflog.Warn(ctx, "Failed to parse routes JSON from state", map[string]interface{}{
			"error": err.Error(),
		})
		return []utils.Route{}
	}

	// Filter routes by gateway
	var filtered []utils.Route
	for _, r := range allRoutes {
		if r.Gateway == gateway {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// diffRoutes compares two sets of routes and returns added and removed routes
func diffRoutes(oldRoutes, newRoutes []utils.Route) (added, removed []utils.Route) {
	// Create maps for quick lookup
	oldMap := make(map[string]utils.Route)
	newMap := make(map[string]utils.Route)

	for _, r := range oldRoutes {
		key := fmt.Sprintf("%s:%s", r.Verb, r.Path)
		oldMap[key] = r
	}

	for _, r := range newRoutes {
		key := fmt.Sprintf("%s:%s", r.Verb, r.Path)
		newMap[key] = r
	}

	// Find added routes (in new but not in old)
	for key, r := range newMap {
		if _, exists := oldMap[key]; !exists {
			added = append(added, r)
		}
	}

	// Find removed routes (in old but not in new)
	for key, r := range oldMap {
		if _, exists := newMap[key]; !exists {
			removed = append(removed, r)
		}
	}

	return added, removed
}
