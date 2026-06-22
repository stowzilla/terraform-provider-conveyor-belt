// internal/datasources/routes.go
package datasources

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource = &routesDataSource{}
)

// NewRoutesDataSource is a helper function to simplify the provider implementation.
func NewRoutesDataSource() datasource.DataSource {
	return &routesDataSource{}
}

// routesDataSource is the data source implementation.
type routesDataSource struct {
	rubyScriptPath            string
}

// routesDataSourceModel maps the data source schema data.
type routesDataSourceModel struct {
	Source types.String `tfsdk:"source"`
	Routes []routeModel `tfsdk:"routes"`
}

// routeModel represents a single route from the JSON output.
type routeModel struct {
	Name    types.String   `tfsdk:"name"`
	Verb    types.String   `tfsdk:"verb"`
	Path    types.String   `tfsdk:"path"`
	Gateway types.String   `tfsdk:"gateway"`
	Lambda  types.String   `tfsdk:"lambda"`
	Auth    types.String   `tfsdk:"auth"`
	Tables  []types.String `tfsdk:"tables"`
}

// Route represents the JSON structure from list_routes.rb
type Route struct {
	Name    string   `json:"name"`
	Verb    string   `json:"verb"`
	Path    string   `json:"path"`
	Gateway string   `json:"gateway"`
	Lambda  string   `json:"lambda"`
	Auth    string   `json:"auth"`
	Tables  []string `json:"tables"`
}

// RouteData represents the top-level JSON structure
type RouteData struct {
	Routes []Route `json:"routes"`
}

// Metadata returns the data source type name.
func (d *routesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_routes"
}

// Schema defines the schema for the data source.
func (d *routesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Data source for reading route definitions from Ruby DSL file.",
		Attributes: map[string]schema.Attribute{
			"source": schema.StringAttribute{
				Description: "Path to the routes.tf.rb file.",
				Required:    true,
			},
			"routes": schema.ListNestedAttribute{
				Description: "List of routes parsed from the DSL.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "Route operation name.",
							Computed:    true,
						},
						"verb": schema.StringAttribute{
							Description: "HTTP method (GET, POST, etc.).",
							Computed:    true,
						},
						"path": schema.StringAttribute{
							Description: "Full route path including namespace prefix.",
							Computed:    true,
						},
						"gateway": schema.StringAttribute{
							Description: "Gateway name that determines which API Gateway handles this route.",
							Computed:    true,
						},
						"lambda": schema.StringAttribute{
							Description: "Lambda function name that handles this route.",
							Computed:    true,
						},
						"auth": schema.StringAttribute{
							Description: "Authorization type (cognito, none, iam).",
							Computed:    true,
						},
						"tables": schema.ListAttribute{
							Description: "DynamoDB tables this route needs access to.",
							Computed:    true,
							ElementType: types.StringType,
						},
					},
				},
			},
		},
	}
}

// Configure prepares the data source.
func (d *routesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	// Extract provider configuration - define interface here to avoid circular import
	if client, ok := req.ProviderData.(interface {
		GetRubyScriptPath() string
	}); ok {
		d.rubyScriptPath = client.GetRubyScriptPath()
	} else {
		// Fallback to default
		d.rubyScriptPath = "../../scripts/list_routes.rb"
	}
}

// Read executes the list_routes.rb script and parses the JSON output.
func (d *routesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config routesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Execute list_routes.rb script
	cmd := exec.Command(d.rubyScriptPath, config.Source.ValueString())
	
	output, err := cmd.Output()
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to execute list_routes.rb",
			"Could not run the Ruby script: "+err.Error(),
		)
		return
	}

	// Parse JSON output
	var routeData RouteData
	err = json.Unmarshal(output, &routeData)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to parse JSON",
			"Could not parse route data from script output: "+err.Error(),
		)
		return
	}

	// Transform to Terraform state
	state := routesDataSourceModel{
		Source: config.Source,
		Routes: make([]routeModel, len(routeData.Routes)),
	}

	for i, route := range routeData.Routes {
		tables := make([]types.String, len(route.Tables))
		for j, table := range route.Tables {
			tables[j] = types.StringValue(table)
		}

		state.Routes[i] = routeModel{
			Name:    types.StringValue(route.Name),
			Verb:    types.StringValue(route.Verb),
			Path:    types.StringValue(route.Path),
			Gateway: types.StringValue(route.Gateway),
			Lambda:  types.StringValue(route.Lambda),
			Auth:    types.StringValue(route.Auth),
			Tables:  tables,
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}