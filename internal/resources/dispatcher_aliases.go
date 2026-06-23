// internal/resources/dispatcher_aliases.go
//
// Aliases for the old "dispatcher" resource type names.
// These allow terraform state mv from dispatcher.* to conveyor_belt.*
// by ensuring the provider can decode state with type "dispatcher",
// "dispatcher_lambda", and "dispatcher_gateway".
//
// Remove these after all environments have been migrated (see CONVEYOR_BELT_RENAME_MIGRATION.md).
package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// dispatcherAlias wraps dispatcherResource with the old "dispatcher" type name.
type dispatcherAlias struct {
	dispatcherResource
}

func NewDispatcherAliasResource() resource.Resource {
	return &dispatcherAlias{}
}

func (r *dispatcherAlias) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "dispatcher"
}

// lambdaAlias wraps lambdaResource with the old "dispatcher_lambda" type name.
type lambdaAlias struct {
	lambdaResource
}

func NewLambdaAliasResource() resource.Resource {
	return &lambdaAlias{}
}

func (r *lambdaAlias) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "dispatcher_lambda"
}

// gatewayAlias wraps gatewayResource with the old "dispatcher_gateway" type name.
type gatewayAlias struct {
	gatewayResource
}

func NewGatewayAliasResource() resource.Resource {
	return &gatewayAlias{}
}

func (r *gatewayAlias) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "dispatcher_gateway"
}
