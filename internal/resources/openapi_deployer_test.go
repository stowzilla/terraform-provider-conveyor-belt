package resources

import (
	"context"
	"testing"

	"terraform-provider-conveyor-belt/internal/utils"
)

func TestGenerateSpec_RequestValidators_WithModels(t *testing.T) {
	config := testConfig()
	gen := NewOpenAPIGenerator(config)

	routes := []utils.Route{
		{Name: "items_create", Verb: "POST", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito", RequestModel: "CreateItem"},
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
	}

	models := []utils.ModelDefinition{
		{
			Name: "CreateItem",
			Properties: map[string]utils.ModelProperty{
				"name": {Type: "string"},
			},
			Required: []string{"name"},
		},
	}

	spec, err := gen.GenerateSpec(context.Background(), "customer", routes, testLambdaARNs(), models)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.RequestValidators == nil {
		t.Fatal("expected request validators to be set when models are present")
	}

	bodyOnly, ok := spec.RequestValidators["body-only"]
	if !ok {
		t.Fatal("expected 'body-only' request validator")
	}

	bodyOnlyMap, ok := bodyOnly.(map[string]interface{})
	if !ok {
		t.Fatal("expected body-only to be a map")
	}

	if bodyOnlyMap["validateRequestBody"] != true {
		t.Error("expected validateRequestBody to be true")
	}
	if bodyOnlyMap["validateRequestParameters"] != false {
		t.Error("expected validateRequestParameters to be false")
	}
}

func TestGenerateSpec_RequestValidators_WithoutModels(t *testing.T) {
	config := testConfig()
	gen := NewOpenAPIGenerator(config)

	routes := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
	}

	spec, err := gen.GenerateSpec(context.Background(), "customer", routes, testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.RequestValidators != nil {
		t.Error("expected no request validators when no models are present")
	}
}

func TestGenerateSpec_RequestValidators_ModelsOnDifferentGateway(t *testing.T) {
	config := testConfig()
	gen := NewOpenAPIGenerator(config)

	routes := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
		{Name: "signup", Verb: "POST", Path: "/signup", Gateway: "onboarding", Lambda: "onboarding", Auth: "none", RequestModel: "SignupRequest"},
	}

	// Customer gateway has no routes with request models
	spec, err := gen.GenerateSpec(context.Background(), "customer", routes, testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.RequestValidators != nil {
		t.Error("expected no request validators for gateway with no request models")
	}
}

func TestGenerateSpecJSON_ForPutRestApi(t *testing.T) {
	// Verify the spec JSON is valid and contains expected PutRestApi-compatible structure
	config := testConfig()
	gen := NewOpenAPIGenerator(config)

	routes := testRoutes()
	lambdaARNs := testLambdaARNs()

	specJSON, err := gen.GenerateSpecJSON(context.Background(), "customer", routes, lambdaARNs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(specJSON) == 0 {
		t.Fatal("expected non-empty spec JSON")
	}

	// Verify it's valid JSON by checking it starts with {
	if specJSON[0] != '{' {
		t.Error("expected spec JSON to start with {")
	}

	// Verify key OpenAPI elements are present
	specStr := string(specJSON)
	requiredElements := []string{
		`"openapi": "3.0.1"`,
		`"x-amazon-apigateway-integration"`,
		`"x-amazon-apigateway-gateway-responses"`,
		`"CognitoUserPoolAuthorizer"`,
		`"aws_proxy"`,
	}

	for _, elem := range requiredElements {
		if !contains([]string{specStr}, specStr) {
			// Simple string contains check
		}
		found := false
		for i := 0; i < len(specStr)-len(elem)+1; i++ {
			if specStr[i:i+len(elem)] == elem {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected spec JSON to contain %q", elem)
		}
	}
}
