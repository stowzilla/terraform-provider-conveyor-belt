package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Fizzy #1878: a known lambda_env_refs map holding an unknown element (a value
// from a resource created in the same apply) must be detected as "not fully
// known" so ModifyPlan defers config-derived hashes instead of keeping the
// prior state's values.
func TestConfigWithUnknownMapElementIsNotFullyKnown(t *testing.T) {
	mapType := tftypes.Map{ElementType: tftypes.String}
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"lambda_env_refs": mapType}}

	known := tftypes.NewValue(objType, map[string]tftypes.Value{
		"lambda_env_refs": tftypes.NewValue(mapType, map[string]tftypes.Value{
			"kms_arn": tftypes.NewValue(tftypes.String, "arn:aws:kms:us-east-1:1:key/a"),
		}),
	})
	if !known.IsFullyKnown() {
		t.Fatal("fully known config reported as not fully known")
	}

	partial := tftypes.NewValue(objType, map[string]tftypes.Value{
		"lambda_env_refs": tftypes.NewValue(mapType, map[string]tftypes.Value{
			"kms_arn": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		}),
	})
	if partial.IsFullyKnown() {
		t.Fatal("known map with unknown element must not be fully known")
	}

	// The map attribute itself is known, so whole-attribute IsUnknown misses it.
	m := types.MapValueMust(types.StringType, map[string]attr.Value{
		"kms_arn": types.StringUnknown(),
	})
	if m.IsUnknown() {
		t.Fatal("map with unknown element should itself be known (this is the bug's trigger)")
	}
}

func TestMarkDerivedHashesUnknown(t *testing.T) {
	known := func() types.Map {
		return types.MapValueMust(types.StringType, map[string]attr.Value{"api": types.StringValue("abc")})
	}
	plan := DispatcherResourceModel{
		RoutesHash:         types.StringValue("r"),
		RoutesJson:         types.StringValue("[]"),
		LambdaHashes:       known(),
		LambdaSourceHashes: known(),
		LambdaConfigHashes: known(),
		GatewayHashes:      known(),
		ModelHashes:        known(),
		OpenAPISpecHashes:  known(),
	}

	markDerivedHashesUnknown(&plan)

	if !plan.RoutesHash.IsUnknown() || !plan.RoutesJson.IsUnknown() {
		t.Error("routes_hash/routes_json must be unknown")
	}
	for name, m := range map[string]types.Map{
		"lambda_hashes":        plan.LambdaHashes,
		"lambda_source_hashes": plan.LambdaSourceHashes,
		"lambda_config_hashes": plan.LambdaConfigHashes,
		"gateway_hashes":       plan.GatewayHashes,
		"model_hashes":         plan.ModelHashes,
		"openapi_spec_hashes":  plan.OpenAPISpecHashes,
	} {
		if !m.IsUnknown() {
			t.Errorf("%s must be unknown", name)
		}
	}
}
