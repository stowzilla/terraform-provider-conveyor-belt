// internal/resources/lambda_resource.go
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
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
	_ resource.Resource                = &lambdaResource{}
	_ resource.ResourceWithConfigure   = &lambdaResource{}
	_ resource.ResourceWithImportState = &lambdaResource{}
)

// NewLambdaResource is a helper function to simplify the provider implementation.
func NewLambdaResource() resource.Resource {
	return &lambdaResource{}
}

// lambdaResource is the resource implementation for individual Lambda functions.
type lambdaResource struct {
	providerConfig    *DispatcherConfig
	clients           *ResourceClients
	iamManager        *IAMManager
	cloudWatchManager *CloudWatchManager
}

// LambdaResourceModel describes the resource data model.
type LambdaResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	AppName      types.String `tfsdk:"app_name"`
	SourceDir    types.String `tfsdk:"source_dir"`
	SharedDirs   types.List   `tfsdk:"shared_dirs"`
	GemDirs      types.List   `tfsdk:"gem_dirs"`
	EnvVars      types.Map    `tfsdk:"env_vars"`
	Timeout      types.Int64  `tfsdk:"timeout"`
	Memory       types.Int64  `tfsdk:"memory"`
	LayerArns    types.List   `tfsdk:"layer_arns"`
	Tables       types.List   `tfsdk:"tables"`
	IamPolicyArns types.List  `tfsdk:"iam_policy_arns"`
	Tags         types.Map    `tfsdk:"tags"`
	SuppressTableEnvVars types.Bool `tfsdk:"suppress_table_env_vars"`
	// Computed outputs
	Arn          types.String `tfsdk:"arn"`
	FunctionName types.String `tfsdk:"function_name"`
	RoleArn      types.String `tfsdk:"role_arn"`
	SourceHash   types.String `tfsdk:"source_hash"`
	ConfigHash   types.String `tfsdk:"config_hash"`
}

// Metadata returns the resource type name.
func (r *lambdaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lambda"
}


// Schema defines the schema for the resource.
func (r *lambdaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single Lambda function with its IAM role and CloudWatch resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Resource identifier",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Lambda function name (without app/env prefix)",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"app_name": schema.StringAttribute{
				Description: "Application name for resource naming",
				Required:    true,
			},
			"source_dir": schema.StringAttribute{
				Description: "Directory containing Lambda source files",
				Required:    true,
			},
			"shared_dirs": schema.ListAttribute{
				Description: "Shared directories to include in package (default: models, lib, helpers, templates)",
				Optional:    true,
				ElementType: types.StringType,
			},
			"gem_dirs": schema.ListAttribute{
				Description: "Directories (relative to source_dir) to include in the Docker gem build context for path-based gems",
				Optional:    true,
				ElementType: types.StringType,
			},
			"env_vars": schema.MapAttribute{
				Description: "Environment variables for the Lambda",
				Optional:    true,
				ElementType: types.StringType,
			},
			"timeout": schema.Int64Attribute{
				Description: "Lambda timeout in seconds. If not specified, uses provider default_lambda_timeout or 30.",
				Optional:    true,
				Computed:    true,
			},
			"memory": schema.Int64Attribute{
				Description: "Lambda memory in MB. If not specified, uses provider default_lambda_memory or 128.",
				Optional:    true,
				Computed:    true,
			},
			"layer_arns": schema.ListAttribute{
				Description: "Lambda Layer ARNs to attach",
				Optional:    true,
				ElementType: types.StringType,
			},
			"tables": schema.ListAttribute{
				Description: "DynamoDB tables this Lambda needs access to",
				Optional:    true,
				ElementType: types.StringType,
			},
			"suppress_table_env_vars": schema.BoolAttribute{
				Description: "When true, do not generate individual *_TABLE_NAME environment variables or the TABLES " +
					"comma-separated list. IAM policies from tables are still created. " +
					"Use this when your Lambda code derives table names from APP_NAME and ENVIRONMENT instead. Default: false.",
				Optional: true,
			},
			"iam_policy_arns": schema.ListAttribute{
				Description: "Additional IAM policy ARNs to attach",
				Optional:    true,
				ElementType: types.StringType,
			},
			"tags": schema.MapAttribute{
				Description: "Tags for this Lambda's resources",
				Optional:    true,
				ElementType: types.StringType,
			},
			// Computed outputs
			"arn": schema.StringAttribute{
				Description: "Lambda function ARN",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"function_name": schema.StringAttribute{
				Description: "Full Lambda function name",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"role_arn": schema.StringAttribute{
				Description: "IAM execution role ARN",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"source_hash": schema.StringAttribute{
				Description: "Hash of source code for change detection",
				Computed:    true,
			},
			"config_hash": schema.StringAttribute{
				Description: "Hash of configuration for change detection",
				Computed:    true,
			},
		},
	}
}


// Configure adds the provider configured client to the resource.
func (r *lambdaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
		Environment:            client.Environment,
		AwsRegion:              client.AwsRegion,
		DefaultLambdaTimeout:   client.DefaultLambdaTimeout,
		DefaultLambdaMemory:    client.DefaultLambdaMemory,
		DefaultTags:            client.DefaultTags,
		DockerBuildConcurrency: client.DockerBuildConcurrency,
	}
}

// getEffectiveTimeout returns the effective timeout value with precedence:
// 1. Resource-level value (if explicitly set)
// 2. Provider-level default (if set)
// 3. Hardcoded default (30)
func (r *lambdaResource) getEffectiveTimeout(model *LambdaResourceModel) int64 {
	// If resource explicitly sets timeout, use it
	if !model.Timeout.IsNull() && !model.Timeout.IsUnknown() {
		return model.Timeout.ValueInt64()
	}
	
	// If provider has a default, use it
	if r.providerConfig != nil && r.providerConfig.DefaultLambdaTimeout > 0 {
		return r.providerConfig.DefaultLambdaTimeout
	}
	
	// Fall back to hardcoded default
	return 30
}

// getEffectiveMemory returns the effective memory value with precedence:
// 1. Resource-level value (if explicitly set)
// 2. Provider-level default (if set)
// 3. Hardcoded default (128)
func (r *lambdaResource) getEffectiveMemory(model *LambdaResourceModel) int64 {
	// If resource explicitly sets memory, use it
	if !model.Memory.IsNull() && !model.Memory.IsUnknown() {
		return model.Memory.ValueInt64()
	}
	
	// If provider has a default, use it
	if r.providerConfig != nil && r.providerConfig.DefaultLambdaMemory > 0 {
		return r.providerConfig.DefaultLambdaMemory
	}
	
	// Fall back to hardcoded default
	return 128
}

// getEffectiveTags returns the effective tags with precedence:
// Resource-level tags override provider-level default tags
func (r *lambdaResource) getEffectiveTags(ctx context.Context, model *LambdaResourceModel) map[string]string {
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

// initializeManagers initializes AWS clients and managers for the resource
func (r *lambdaResource) initializeManagers(ctx context.Context, config *DispatcherConfig) error {
	if r.clients != nil {
		return nil // Already initialized
	}

	clients, err := initializeClients(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to initialize AWS clients: %w", err)
	}

	r.clients = clients
	r.iamManager = NewIAMManager(clients.IAM, config)
	r.cloudWatchManager = NewCloudWatchManager(clients.CloudWatchLogs, clients.CloudWatch, config)

	return nil
}

// buildConfigFromModel builds a DispatcherConfig from the resource model
func (r *lambdaResource) buildConfigFromModel(ctx context.Context, model *LambdaResourceModel) (*DispatcherConfig, error) {
	config := &DispatcherConfig{
		Environment:     r.providerConfig.Environment,
		AwsRegion:       r.providerConfig.AwsRegion,
		AppName:         model.AppName.ValueString(),
		LambdaSourceDir: model.SourceDir.ValueString(),
	}

	// Get AWS account ID
	accountId, err := getAwsAccountId(ctx, config.AwsRegion)
	if err != nil {
		return nil, fmt.Errorf("failed to get AWS account ID: %w", err)
	}
	config.AwsAccountId = accountId

	// Extract shared directories
	if !model.SharedDirs.IsNull() && !model.SharedDirs.IsUnknown() {
		var sharedDirs []string
		diags := model.SharedDirs.ElementsAs(ctx, &sharedDirs, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract shared_dirs")
		}
		config.LambdaSharedDirs = sharedDirs
	}

	// Extract gem directories
	if !model.GemDirs.IsNull() && !model.GemDirs.IsUnknown() {
		var gemDirs []string
		diags := model.GemDirs.ElementsAs(ctx, &gemDirs, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract gem_dirs")
		}
		config.LambdaGemDirs = gemDirs
	}

	// Extract layer ARNs
	if !model.LayerArns.IsNull() && !model.LayerArns.IsUnknown() {
		var layerArns []string
		diags := model.LayerArns.ElementsAs(ctx, &layerArns, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract layer_arns")
		}
		config.LambdaLayerArns = layerArns
	}

	// Extract IAM policy ARNs
	if !model.IamPolicyArns.IsNull() && !model.IamPolicyArns.IsUnknown() {
		var policyArns []string
		diags := model.IamPolicyArns.ElementsAs(ctx, &policyArns, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to extract iam_policy_arns")
		}
		config.SharedIamPolicyArns = policyArns
	}

	// Extract tags with configuration precedence (resource tags override provider default tags)
	config.Tags = r.getEffectiveTags(ctx, model)

	// Extract suppress_table_env_vars setting (defaults to false)
	if !model.SuppressTableEnvVars.IsNull() && !model.SuppressTableEnvVars.IsUnknown() {
		config.SuppressTableEnvVars = model.SuppressTableEnvVars.ValueBool()
	}

	return config, nil
}


// Create creates the resource and sets the initial Terraform state.
func (r *lambdaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LambdaResourceModel
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

	lambdaName := plan.Name.ValueString()
	functionName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, lambdaName)
	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", config.AppName, config.Environment, lambdaName)

	utils.Info(ctx, "Creating Lambda resource", map[string]interface{}{
		"name":          lambdaName,
		"function_name": functionName,
	})

	// Step 1: Create CloudWatch log group
	utils.Info(ctx, "Creating CloudWatch log group...")
	if err := r.cloudWatchManager.CreateLambdaLogGroup(ctx, functionName); err != nil {
		utils.Warn(ctx, "Failed to create CloudWatch log group", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
		// Continue anyway - Lambda will create it automatically
	}

	// Step 2: Create IAM execution role
	utils.Info(ctx, "Creating IAM execution role...")
	roleArn, err := r.iamManager.CreateLambdaExecutionRole(ctx, roleName, lambdaName)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to create IAM role",
			"Could not create Lambda execution role: "+err.Error(),
		)
		return
	}

	// Attach additional IAM policies if specified
	if err := r.iamManager.AttachSharedIamPolicies(ctx, roleName); err != nil {
		resp.Diagnostics.AddError(
			"Failed to attach shared IAM policies",
			fmt.Sprintf("Could not attach shared IAM policies to role %s: %s", roleName, err.Error()),
		)
		return
	}

	// Attach VPC execution policy if this lambda has vpc_config
	if extractVpcConfig(config.LambdaConfig, lambdaName) != nil {
		if err := r.iamManager.AttachVpcExecutionPolicy(ctx, roleName); err != nil {
			utils.Warn(ctx, "Failed to attach VPC execution policy", map[string]interface{}{
				"role_name": roleName,
				"error":     err.Error(),
			})
		}
	}

	// Step 3: Build Lambda package using PackageBuilder
	utils.Info(ctx, "Building Lambda package...")
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	packageBuilder := NewPackageBuilder(
		config.LambdaSourceDir,
		WithSharedDirs(sharedDirs),
		WithGemDirs(config.LambdaGemDirs),
		WithConfig(config),
	)

	results := packageBuilder.BuildPackages(ctx, []string{lambdaName})
	result, exists := results[lambdaName]
	if !exists || result.Error != nil {
		errMsg := "unknown error"
		if result != nil && result.Error != nil {
			errMsg = result.Error.Error()
		}
		// Rollback: delete IAM role
		r.iamManager.DeleteLambdaRole(ctx, roleName, lambdaName)
		r.cloudWatchManager.DeleteLambdaLogGroup(ctx, functionName)

		resp.Diagnostics.AddError(
			"Failed to build Lambda package",
			"Could not build Lambda deployment package: "+errMsg,
		)
		return
	}

	// Step 4: Create Lambda function
	utils.Info(ctx, "Creating Lambda function...")
	envVars := r.buildEnvVars(ctx, &plan, config)

	// Get effective timeout and memory with configuration precedence
	effectiveTimeout := r.getEffectiveTimeout(&plan)
	effectiveMemory := r.getEffectiveMemory(&plan)

	lambdaArn, err := r.createLambdaFunction(ctx, functionName, roleArn, lambdaName, result.ZipData, envVars, config, effectiveTimeout, effectiveMemory)
	if err != nil {
		// Rollback: delete IAM role and log group
		r.iamManager.DeleteLambdaRole(ctx, roleName, lambdaName)
		r.cloudWatchManager.DeleteLambdaLogGroup(ctx, functionName)

		resp.Diagnostics.AddError(
			"Failed to create Lambda function",
			"Could not create Lambda function: "+err.Error(),
		)
		return
	}

	// Step 5: Create CloudWatch alarms
	utils.Info(ctx, "Creating CloudWatch alarms...")
	if err := r.cloudWatchManager.CreateOrUpdateLambdaAlarms(ctx, functionName, lambdaName); err != nil {
		utils.Warn(ctx, "Failed to create CloudWatch alarms", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
		// Continue anyway - alarms are optional
	}

	// Step 6: Create DynamoDB policies if tables are specified
	if !plan.Tables.IsNull() && !plan.Tables.IsUnknown() {
		var tables []string
		diags := plan.Tables.ElementsAs(ctx, &tables, false)
		if !diags.HasError() && len(tables) > 0 {
			routes := r.buildRoutesFromTables(lambdaName, tables)
			if err := r.iamManager.CreateDynamoDBPoliciesForAction(ctx, lambdaName, routes, roleName); err != nil {
				utils.Warn(ctx, "Failed to create DynamoDB policies", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}
		}
	}

	// Calculate hashes for change detection
	sourceHash, err := calculateLambdaSourceHash(config.LambdaSourceDir, lambdaName, sharedDirs, config.LambdaGemDirs...)
	if err != nil {
		utils.Warn(ctx, "Failed to calculate source hash", map[string]interface{}{
			"error": err.Error(),
		})
		sourceHash = ""
	}

	configHash := r.calculateConfigHashForModel(ctx, &plan)

	// Populate computed outputs
	plan.ID = types.StringValue(functionName)
	plan.Arn = types.StringValue(lambdaArn)
	plan.FunctionName = types.StringValue(functionName)
	plan.RoleArn = types.StringValue(roleArn)
	plan.SourceHash = types.StringValue(sourceHash)
	plan.ConfigHash = types.StringValue(configHash)

	utils.Info(ctx, "Successfully created Lambda resource", map[string]interface{}{
		"function_name": functionName,
		"arn":           lambdaArn,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// createLambdaFunction creates the Lambda function in AWS
func (r *lambdaResource) createLambdaFunction(ctx context.Context, functionName, roleArn, lambdaName string, zipData []byte, envVars map[string]string, config *DispatcherConfig, timeout, memory int64) (string, error) {
	// Use provided timeout/memory values (which already have defaults from schema or provider)
	timeoutInt32 := int32(timeout)
	memoryInt32 := int32(memory)

	createInput := &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Runtime:      lambdaTypes.RuntimeRuby34,
		Role:         aws.String(roleArn),
		Handler:      aws.String(fmt.Sprintf("%s.lambda_handler", lambdaName)),
		Code: &lambdaTypes.FunctionCode{
			ZipFile: zipData,
		},
		Description: aws.String(fmt.Sprintf("Lambda function for %s in %s-%s", lambdaName, config.AppName, config.Environment)),
		Environment: &lambdaTypes.Environment{
			Variables: envVars,
		},
		Tags:       buildResourceTags(config, "Lambda", functionName),
		Timeout:    aws.Int32(timeoutInt32),
		MemorySize: aws.Int32(memoryInt32),
	}

	// Attach Lambda Layers if specified
	if len(config.LambdaLayerArns) > 0 {
		createInput.Layers = config.LambdaLayerArns
	}

	// Attach VPC configuration if specified
	vpcConfig := extractVpcConfig(config.LambdaConfig, lambdaName)
	if vpcConfig != nil {
		createInput.VpcConfig = &lambdaTypes.VpcConfig{
			SubnetIds:        vpcConfig.SubnetIds,
			SecurityGroupIds: vpcConfig.SecurityGroupIds,
		}
	}

	// Retry logic for IAM role propagation
	var createErr error
	for attempt := 0; attempt < 10; attempt++ {
		output, err := r.clients.Lambda.CreateFunction(ctx, createInput)
		if err == nil {
			return *output.FunctionArn, nil
		}

		createErr = err

		// Check if function already exists
		if strings.Contains(err.Error(), "ResourceConflictException") {
			// Function exists, get its ARN
			getOutput, getErr := r.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
				FunctionName: aws.String(functionName),
			})
			if getErr == nil {
				return *getOutput.Configuration.FunctionArn, nil
			}
			return "", fmt.Errorf("function exists but failed to get ARN: %w", getErr)
		}

		// Check if IAM role propagation error (includes KMS grant failures)
		if strings.Contains(err.Error(), "cannot be assumed by Lambda") ||
			strings.Contains(err.Error(), "KMS key is invalid for CreateGrant") ||
			strings.Contains(err.Error(), "ARN does not refer to a valid principal") {
			utils.Info(ctx, "IAM role not yet propagated, retrying...", map[string]interface{}{
				"attempt":      attempt + 1,
				"error_detail": err.Error(),
			})
			time.Sleep(time.Duration(attempt+1) * time.Second) // Exponential backoff
			continue
		}

		// Other error - return immediately
		return "", err
	}

	return "", fmt.Errorf("failed to create Lambda function after retries: %w", createErr)
}

// buildEnvVars builds environment variables for the Lambda function
func (r *lambdaResource) buildEnvVars(ctx context.Context, model *LambdaResourceModel, config *DispatcherConfig) map[string]string {
	envVars := map[string]string{
		"APP_NAME":    config.AppName,
		"ENVIRONMENT": config.Environment,
		"ACTION":      model.Name.ValueString(),
	}

	// Add user-specified env vars
	if !model.EnvVars.IsNull() && !model.EnvVars.IsUnknown() {
		userEnvVars := make(map[string]string)
		diags := model.EnvVars.ElementsAs(ctx, &userEnvVars, false)
		if !diags.HasError() {
			for k, v := range userEnvVars {
				envVars[k] = v
			}
		}
	}

	// Add table environment variables (unless suppressed)
	if !config.SuppressTableEnvVars && !model.Tables.IsNull() && !model.Tables.IsUnknown() {
		var tables []string
		diags := model.Tables.ElementsAs(ctx, &tables, false)
		if !diags.HasError() {
			for _, table := range tables {
				normalizedTable := strings.ReplaceAll(table, "_", "-")
				fullTableName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, normalizedTable)
				envVarName := strings.ToUpper(table) + "_TABLE_NAME"
				envVars[envVarName] = fullTableName
			}
			if len(tables) > 0 {
				fullTableNames := make([]string, len(tables))
				for i, table := range tables {
					normalizedTable := strings.ReplaceAll(table, "_", "-")
					fullTableNames[i] = fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, normalizedTable)
				}
				envVars["TABLES"] = strings.Join(fullTableNames, ",")
			}
		}
	}

	return envVars
}

// buildRoutesFromTables creates route structures for DynamoDB policy creation
func (r *lambdaResource) buildRoutesFromTables(lambdaName string, tables []string) []utils.Route {
	routes := make([]utils.Route, 0, len(tables))
	for _, table := range tables {
		routes = append(routes, utils.Route{
			Lambda: lambdaName,
			Tables: []string{table},
		})
	}
	return routes
}

// calculateConfigHashForModel calculates a hash of the configuration for change detection
func (r *lambdaResource) calculateConfigHashForModel(ctx context.Context, model *LambdaResourceModel) string {
	// Build a simple hash from the configuration values
	var parts []string

	if !model.EnvVars.IsNull() && !model.EnvVars.IsUnknown() {
		envVars := make(map[string]string)
		model.EnvVars.ElementsAs(ctx, &envVars, false)
		for k, v := range envVars {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
	}

	if !model.Tables.IsNull() && !model.Tables.IsUnknown() {
		var tables []string
		model.Tables.ElementsAs(ctx, &tables, false)
		parts = append(parts, strings.Join(tables, ","))
	}

	if !model.LayerArns.IsNull() && !model.LayerArns.IsUnknown() {
		var layers []string
		model.LayerArns.ElementsAs(ctx, &layers, false)
		parts = append(parts, strings.Join(layers, ","))
	}

	parts = append(parts, fmt.Sprintf("timeout=%d", model.Timeout.ValueInt64()))
	parts = append(parts, fmt.Sprintf("memory=%d", model.Memory.ValueInt64()))

	combined := strings.Join(parts, "|")
	return fmt.Sprintf("%x", combined)
}


// Read reads the resource state from AWS.
func (r *lambdaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LambdaResourceModel
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

	functionName := state.FunctionName.ValueString()
	if functionName == "" {
		functionName = fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, state.Name.ValueString())
	}

	utils.Info(ctx, "Reading Lambda resource", map[string]interface{}{
		"function_name": functionName,
	})

	// Read Lambda function from AWS
	getOutput, err := r.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String(functionName),
	})
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			// Resource no longer exists
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Failed to read Lambda function",
			"Could not read Lambda function from AWS: "+err.Error(),
		)
		return
	}

	// Update state with current values from AWS
	state.Arn = types.StringValue(*getOutput.Configuration.FunctionArn)
	state.FunctionName = types.StringValue(*getOutput.Configuration.FunctionName)
	state.RoleArn = types.StringValue(*getOutput.Configuration.Role)

	// Recalculate hashes
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	sourceHash, err := calculateLambdaSourceHash(config.LambdaSourceDir, state.Name.ValueString(), sharedDirs, config.LambdaGemDirs...)
	if err != nil {
		utils.Warn(ctx, "Failed to calculate source hash", map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		state.SourceHash = types.StringValue(sourceHash)
	}

	state.ConfigHash = types.StringValue(r.calculateConfigHashForModel(ctx, &state))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}


// Update updates the resource and sets the updated Terraform state.
func (r *lambdaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan LambdaResourceModel
	var state LambdaResourceModel

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

	lambdaName := plan.Name.ValueString()
	functionName := state.FunctionName.ValueString()
	if functionName == "" {
		functionName = fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, lambdaName)
	}

	utils.Info(ctx, "Updating Lambda resource", map[string]interface{}{
		"function_name": functionName,
	})

	// Calculate new hashes to detect what changed
	sharedDirs := config.LambdaSharedDirs
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}

	newSourceHash, err := calculateLambdaSourceHash(config.LambdaSourceDir, lambdaName, sharedDirs, config.LambdaGemDirs...)
	if err != nil {
		utils.Warn(ctx, "Failed to calculate source hash", map[string]interface{}{
			"error": err.Error(),
		})
		newSourceHash = ""
	}

	newConfigHash := r.calculateConfigHashForModel(ctx, &plan)

	sourceChanged := newSourceHash != state.SourceHash.ValueString()
	configChanged := newConfigHash != state.ConfigHash.ValueString()

	utils.Info(ctx, "Detected changes", map[string]interface{}{
		"source_changed": sourceChanged,
		"config_changed": configChanged,
	})

	// Update Lambda code if source changed
	if sourceChanged {
		utils.Info(ctx, "Source code changed, rebuilding package...")

		packageBuilder := NewPackageBuilder(
			config.LambdaSourceDir,
			WithSharedDirs(sharedDirs),
			WithGemDirs(config.LambdaGemDirs),
			WithConfig(config),
		)

		results := packageBuilder.BuildPackages(ctx, []string{lambdaName})
		result, exists := results[lambdaName]
		if !exists || result.Error != nil {
			errMsg := "unknown error"
			if result != nil && result.Error != nil {
				errMsg = result.Error.Error()
			}
			resp.Diagnostics.AddError(
				"Failed to build Lambda package",
				"Could not build Lambda deployment package: "+errMsg,
			)
			return
		}

		// Update function code
		_, err := r.clients.Lambda.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
			FunctionName: aws.String(functionName),
			ZipFile:      result.ZipData,
		})
		if err != nil {
			resp.Diagnostics.AddError(
				"Failed to update Lambda code",
				"Could not update Lambda function code: "+err.Error(),
			)
			return
		}

		utils.Info(ctx, "Successfully updated Lambda code")
	}

	// Update Lambda configuration if config changed
	if configChanged {
		utils.Info(ctx, "Configuration changed, updating Lambda configuration...")

		envVars := r.buildEnvVars(ctx, &plan, config)

		// Get effective timeout and memory with configuration precedence
		effectiveTimeout := r.getEffectiveTimeout(&plan)
		effectiveMemory := r.getEffectiveMemory(&plan)

		updateConfigInput := &lambda.UpdateFunctionConfigurationInput{
			FunctionName: aws.String(functionName),
			Environment: &lambdaTypes.Environment{
				Variables: envVars,
			},
			Timeout:    aws.Int32(int32(effectiveTimeout)),
			MemorySize: aws.Int32(int32(effectiveMemory)),
		}

		// Update layers if specified
		if len(config.LambdaLayerArns) > 0 {
			updateConfigInput.Layers = config.LambdaLayerArns
		}

		// Update VPC configuration if specified
		vpcConfig := extractVpcConfig(config.LambdaConfig, lambdaName)
		if vpcConfig != nil {
			updateConfigInput.VpcConfig = &lambdaTypes.VpcConfig{
				SubnetIds:        vpcConfig.SubnetIds,
				SecurityGroupIds: vpcConfig.SecurityGroupIds,
			}
		}

		_, err := r.clients.Lambda.UpdateFunctionConfiguration(ctx, updateConfigInput)
		if err != nil {
			resp.Diagnostics.AddError(
				"Failed to update Lambda configuration",
				"Could not update Lambda function configuration: "+err.Error(),
			)
			return
		}

		utils.Info(ctx, "Successfully updated Lambda configuration")
	}

	// Update CloudWatch alarms when config changes
	// PutMetricAlarm is idempotent - it creates or updates the alarm
	if configChanged {
		if err := r.cloudWatchManager.CreateOrUpdateLambdaAlarms(ctx, functionName, lambdaName); err != nil {
			utils.Warn(ctx, "Failed to update CloudWatch alarms", map[string]interface{}{
				"function_name": functionName,
				"error":         err.Error(),
			})
			// Don't fail the update - alarm update is best-effort
		}
	}

	// Update IAM policies if tables changed
	if !plan.Tables.Equal(state.Tables) {
		utils.Info(ctx, "Tables changed, updating IAM policies...")

		roleName := fmt.Sprintf("%s-%s-%s-lambda-role", config.AppName, config.Environment, lambdaName)

		var tables []string
		if !plan.Tables.IsNull() && !plan.Tables.IsUnknown() {
			plan.Tables.ElementsAs(ctx, &tables, false)
		}

		if len(tables) > 0 {
			routes := r.buildRoutesFromTables(lambdaName, tables)
			if err := r.iamManager.CreateDynamoDBPoliciesForAction(ctx, lambdaName, routes, roleName); err != nil {
				utils.Warn(ctx, "Failed to update DynamoDB policies", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}
		}
	}

	// Update computed values
	plan.ID = state.ID
	plan.Arn = state.Arn
	plan.FunctionName = state.FunctionName
	plan.RoleArn = state.RoleArn
	plan.SourceHash = types.StringValue(newSourceHash)
	plan.ConfigHash = types.StringValue(newConfigHash)

	utils.Info(ctx, "Successfully updated Lambda resource", map[string]interface{}{
		"function_name": functionName,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}


// Delete deletes the resource and removes the Terraform state.
func (r *lambdaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LambdaResourceModel
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

	lambdaName := state.Name.ValueString()
	functionName := state.FunctionName.ValueString()
	if functionName == "" {
		functionName = fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, lambdaName)
	}
	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", config.AppName, config.Environment, lambdaName)

	utils.Info(ctx, "Deleting Lambda resource", map[string]interface{}{
		"function_name": functionName,
	})

	// Step 1: Delete CloudWatch alarms
	utils.Info(ctx, "Deleting CloudWatch alarms...")
	if err := r.cloudWatchManager.DeleteLambdaAlarms(ctx, functionName); err != nil {
		utils.Warn(ctx, "Failed to delete CloudWatch alarms", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
		// Continue with deletion
	}

	// Step 2: Delete CloudWatch log group
	utils.Info(ctx, "Deleting CloudWatch log group...")
	if err := r.cloudWatchManager.DeleteLambdaLogGroup(ctx, functionName); err != nil {
		utils.Warn(ctx, "Failed to delete CloudWatch log group", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
		// Continue with deletion
	}

	// Step 3: Delete Lambda function
	utils.Info(ctx, "Deleting Lambda function...")
	_, err = r.clients.Lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
		FunctionName: aws.String(functionName),
	})
	if err != nil {
		if !strings.Contains(err.Error(), "ResourceNotFoundException") {
			resp.Diagnostics.AddError(
				"Failed to delete Lambda function",
				"Could not delete Lambda function: "+err.Error(),
			)
			return
		}
		utils.Info(ctx, "Lambda function does not exist, skipping deletion")
	}

	// Step 4: Delete IAM role and policies
	utils.Info(ctx, "Deleting IAM role and policies...")
	if err := r.iamManager.DeleteLambdaRole(ctx, roleName, lambdaName); err != nil {
		utils.Warn(ctx, "Failed to delete IAM role", map[string]interface{}{
			"role_name": roleName,
			"error":     err.Error(),
		})
		// Continue - role might not exist
	}

	utils.Info(ctx, "Successfully deleted Lambda resource", map[string]interface{}{
		"function_name": functionName,
	})
}


// ImportState imports an existing Lambda function into Terraform state.
func (r *lambdaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The import ID can be either:
	// 1. The full function name (e.g., "myapp-dev-customer")
	// 2. The Lambda ARN (e.g., "arn:aws:lambda:us-east-1:123456789012:function:myapp-dev-customer")

	importID := req.ID

	// Extract function name from ARN if provided
	functionName := importID
	if strings.HasPrefix(importID, "arn:aws:lambda:") {
		// Parse ARN: arn:aws:lambda:region:account:function:name
		parts := strings.Split(importID, ":")
		if len(parts) >= 7 {
			functionName = parts[6]
		}
	}

	utils.Info(ctx, "Importing Lambda resource", map[string]interface{}{
		"import_id":     importID,
		"function_name": functionName,
	})

	// Set the ID attribute for the Read method to use
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), functionName)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("function_name"), functionName)...)

	// Parse the function name to extract name, app_name
	// Expected format: {app_name}-{environment}-{name}
	// We need to extract these from the function name
	parts := strings.Split(functionName, "-")
	if len(parts) >= 3 {
		// Assume format: app-env-name (where name might contain hyphens)
		// We'll set app_name and name, but the user may need to adjust
		appName := parts[0]
		// The rest after app-env is the lambda name
		lambdaName := strings.Join(parts[2:], "-")

		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_name"), appName)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), lambdaName)...)
	}

	// Note: source_dir must be provided by the user after import
	// as we cannot determine it from AWS
}

