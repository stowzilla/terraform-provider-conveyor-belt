// internal/provider/provider.go
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/datasources"
	"terraform-provider-conveyor-belt/internal/resources"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ provider.Provider = &dispatcherProvider{}
)

// New is a helper function to simplify provider server and testing implementation.
func New() provider.Provider {
	return &dispatcherProvider{}
}

// dispatcherProvider is the provider implementation.
type dispatcherProvider struct{}

// Metadata returns the provider type name.
func (p *dispatcherProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "conveyor_belt"
}

// Schema defines the provider-level schema for configuration data.
func (p *dispatcherProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Dispatcher provider for AWS serverless infrastructure",
		Attributes: map[string]schema.Attribute{
			"environment": schema.StringAttribute{
				Required:    true,
				Description: "Environment name (dev01, dev02, staging, prod).",
			},
			"aws_region": schema.StringAttribute{
				Required:    true,
				Description: "AWS region.",
			},
			"ruby_script_path": schema.StringAttribute{
				Optional:    true,
				Description: "Deprecated: no longer used. Routes are now parsed via the `belt` CLI gem.",
			},
			"default_lambda_timeout": schema.Int64Attribute{
				Optional:    true,
				Description: "Default Lambda timeout in seconds. Applied to all dispatcher_lambda resources unless overridden at the resource level. Default: 30.",
			},
			"default_lambda_memory": schema.Int64Attribute{
				Optional:    true,
				Description: "Default Lambda memory in MB. Applied to all dispatcher_lambda resources unless overridden at the resource level. Default: 128.",
			},
			"default_tags": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Default tags to apply to all resources created by this provider. Resource-level tags will be merged with these defaults, with resource-level tags taking precedence.",
			},
			"docker_build_concurrency": schema.Int64Attribute{
				Optional:    true,
				Description: "Maximum number of concurrent Docker builds for Lambda packages. Default: number of CPUs.",
			},
		},
	}
}

// dispatcherProviderModel maps provider schema data to a Go type.
type dispatcherProviderModel struct {
	Environment               types.String `tfsdk:"environment"`
	AwsRegion                 types.String `tfsdk:"aws_region"`
	RubyScriptPath            types.String `tfsdk:"ruby_script_path"`
	DefaultLambdaTimeout      types.Int64  `tfsdk:"default_lambda_timeout"`
	DefaultLambdaMemory       types.Int64  `tfsdk:"default_lambda_memory"`
	DefaultTags               types.Map    `tfsdk:"default_tags"`
	DockerBuildConcurrency    types.Int64  `tfsdk:"docker_build_concurrency"`
}

// Configure prepares a Dispatcher provider.
func (p *dispatcherProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config dispatcherProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if diags.HasError() {
		return
	}

	// Extract default tags
	var defaultTags map[string]string
	if !config.DefaultTags.IsNull() && !config.DefaultTags.IsUnknown() {
		defaultTags = make(map[string]string)
		diags := config.DefaultTags.ElementsAs(ctx, &defaultTags, false)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// Get docker build concurrency (default to 0 which means use CPU count)
	dockerBuildConcurrency := int(config.DockerBuildConcurrency.ValueInt64())

	client := &resources.DispatcherClient{
		Environment:            config.Environment.ValueString(),
		AwsRegion:              config.AwsRegion.ValueString(),
		DefaultLambdaTimeout:   config.DefaultLambdaTimeout.ValueInt64(),
		DefaultLambdaMemory:    config.DefaultLambdaMemory.ValueInt64(),
		DefaultTags:            defaultTags,
		DockerBuildConcurrency: dockerBuildConcurrency,
	}

	resp.DataSourceData = client
	resp.ResourceData = client
}

// DataSources defines the data sources implemented in the provider.
func (p *dispatcherProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		datasources.NewRoutesDataSource,
	}
}

// Resources defines the resources implemented in the provider.
// The provider exposes three resource types:
// - conveyor_belt: The primary orchestrator resource that manages all infrastructure from routes.tf.rb
// - conveyor_belt_lambda: Manages individual Lambda functions (for advanced users who need fine-grained control)
// - conveyor_belt_gateway: Manages individual API Gateways (for advanced users who need fine-grained control)
//
// Legacy aliases (dispatcher, dispatcher_lambda, dispatcher_gateway) are registered
// to support terraform state mv during migration. Remove after all environments are migrated.
func (p *dispatcherProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewDispatcherResource,
		resources.NewLambdaResource,
		resources.NewGatewayResource,
		// Legacy aliases for migration (see CONVEYOR_BELT_RENAME_MIGRATION.md)
		resources.NewDispatcherAliasResource,
		resources.NewLambdaAliasResource,
		resources.NewGatewayAliasResource,
	}
}

// DispatcherClient holds the configuration for the provider.
type DispatcherClient struct {
	AppName             string
	Environment         string
	AwsRegion           string
	AwsAccountId        string
	FrontendUrls        []string
	LambdaSourceDir     string
	CognitoUserPoolArns []string
	LambdaEnvVars       map[string]interface{}
	S3Buckets           map[string]interface{}
	LambdaConfig        map[string]interface{}
	SharedIamPolicyArns []string
	LambdaSharedDirs    []string
}
