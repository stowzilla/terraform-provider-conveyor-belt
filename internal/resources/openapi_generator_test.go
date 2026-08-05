package resources

import (
	"context"
	"encoding/json"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"terraform-provider-conveyor-belt/internal/utils"
)

func testConfig() *DispatcherConfig {
	return &DispatcherConfig{
		AppName:             "myapp",
		Environment:         "dev",
		AwsRegion:           "us-east-1",
		AwsAccountId:        "123456789012",
		FrontendUrls:        []string{"https://app.example.com"},
		CognitoUserPoolArns: []string{"arn:aws:cognito-idp:us-east-1:123456789012:userpool/us-east-1_abc123"},
		FriendlyErrors:      true,
	}
}

func testRoutes() []utils.Route {
	return []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
		{Name: "items_show", Verb: "GET", Path: "/items/{id}", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
		{Name: "items_create", Verb: "POST", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
		{Name: "health", Verb: "GET", Path: "/health", Gateway: "ops", Lambda: "ops", Auth: "none"},
		{Name: "containers_index", Verb: "GET", Path: "/containers", Gateway: "ops", Lambda: "ops", Auth: "cognito"},
		{Name: "signup", Verb: "POST", Path: "/signup", Gateway: "onboarding", Lambda: "onboarding", Auth: "none"},
	}
}

func testLambdaARNs() map[string]string {
	return map[string]string{
		"customer":    "arn:aws:lambda:us-east-1:123456789012:function:myapp-dev-customer",
		"ops":         "arn:aws:lambda:us-east-1:123456789012:function:myapp-dev-ops",
		"onboarding":  "arn:aws:lambda:us-east-1:123456789012:function:myapp-dev-onboarding",
	}
}

func TestGenerateSpec_BasicStructure(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.OpenAPI != "3.0.1" {
		t.Errorf("expected openapi 3.0.1, got %s", spec.OpenAPI)
	}
	if spec.Info.Title != "myapp-dev-customer" {
		t.Errorf("expected title myapp-dev-customer, got %s", spec.Info.Title)
	}
	if len(spec.Paths) != 2 {
		t.Errorf("expected 2 paths (/items, /items/{id}), got %d", len(spec.Paths))
	}
}

func TestGenerateSpec_CognitoAuth(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.Components == nil || spec.Components.SecuritySchemes == nil {
		t.Fatal("expected security schemes for cognito routes")
	}
	auth, ok := spec.Components.SecuritySchemes["CognitoUserPoolAuthorizer"]
	if !ok {
		t.Fatal("expected CognitoUserPoolAuthorizer in security schemes")
	}
	authMap := auth.(map[string]interface{})
	if authMap["type"] != "apiKey" {
		t.Errorf("expected apiKey type, got %v", authMap["type"])
	}

	// Check that GET /items has security
	itemsPath := spec.Paths["/items"]
	getOp := itemsPath["get"].(map[string]interface{})
	security, ok := getOp["security"]
	if !ok {
		t.Fatal("expected security on cognito route")
	}
	secList := security.([]map[string]interface{})
	if _, ok := secList[0]["CognitoUserPoolAuthorizer"]; !ok {
		t.Error("expected CognitoUserPoolAuthorizer in security")
	}
}

func TestGenerateSpec_NoAuth(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "onboarding", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signupPath := spec.Paths["/signup"]
	postOp := signupPath["post"].(map[string]interface{})
	if _, ok := postOp["security"]; ok {
		t.Error("expected no security on auth=none route")
	}

	// No cognito routes → no security schemes
	if spec.Components != nil && spec.Components.SecuritySchemes != nil {
		t.Error("expected no security schemes when no cognito routes")
	}
}

func TestGenerateSpec_CORSOptions(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// /items has GET and POST, so OPTIONS should allow GET,OPTIONS,POST
	itemsPath := spec.Paths["/items"]
	optionsOp, ok := itemsPath["options"]
	if !ok {
		t.Fatal("expected OPTIONS method on /items")
	}

	optionsMap := optionsOp.(map[string]interface{})
	integration := optionsMap["x-amazon-apigateway-integration"].(map[string]interface{})
	if integration["type"] != "mock" {
		t.Errorf("expected mock integration for OPTIONS, got %v", integration["type"])
	}

	responses := integration["responses"].(map[string]interface{})
	defaultResp := responses["default"].(map[string]interface{})
	respParams := defaultResp["responseParameters"].(map[string]string)
	allowMethods := respParams["method.response.header.Access-Control-Allow-Methods"]
	if allowMethods != "'GET,OPTIONS,POST'" {
		t.Errorf("expected 'GET,OPTIONS,POST', got %s", allowMethods)
	}

	allowOrigin := respParams["method.response.header.Access-Control-Allow-Origin"]
	if allowOrigin != "'https://app.example.com'" {
		t.Errorf("expected 'https://app.example.com', got %s", allowOrigin)
	}
}

func TestGenerateSpec_LambdaIntegration(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "ops", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	healthPath := spec.Paths["/health"]
	getOp := healthPath["get"].(map[string]interface{})
	integration := getOp["x-amazon-apigateway-integration"].(map[string]interface{})

	if integration["type"] != "aws_proxy" {
		t.Errorf("expected aws_proxy, got %v", integration["type"])
	}
	if integration["httpMethod"] != "POST" {
		t.Errorf("expected POST, got %v", integration["httpMethod"])
	}

	expectedUri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:123456789012:function:myapp-dev-ops/invocations"
	if integration["uri"] != expectedUri {
		t.Errorf("expected uri %s, got %v", expectedUri, integration["uri"])
	}
}

func TestGenerateSpec_PathParameters(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	itemPath := spec.Paths["/items/{id}"]
	getOp := itemPath["get"].(map[string]interface{})
	params, ok := getOp["parameters"]
	if !ok {
		t.Fatal("expected parameters on /items/{id}")
	}
	paramList := params.([]map[string]interface{})
	if len(paramList) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(paramList))
	}
	if paramList[0]["name"] != "id" {
		t.Errorf("expected parameter name 'id', got %v", paramList[0]["name"])
	}
	if paramList[0]["in"] != "path" {
		t.Errorf("expected 'path', got %v", paramList[0]["in"])
	}
}

func TestGenerateSpec_GatewayResponses(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "ops", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.GatewayResponses == nil {
		t.Fatal("expected gateway responses")
	}

	expectedKeys := []string{
		"UNAUTHORIZED", "ACCESS_DENIED", "DEFAULT_4XX", "DEFAULT_5XX",
		"MISSING_AUTHENTICATION_TOKEN", "RESOURCE_NOT_FOUND", "BAD_REQUEST_BODY",
	}
	for _, key := range expectedKeys {
		if _, ok := spec.GatewayResponses[key]; !ok {
			t.Errorf("expected gateway response %s", key)
		}
	}

	// MISSING_AUTHENTICATION_TOKEN should have statusCode 404
	mat := spec.GatewayResponses["MISSING_AUTHENTICATION_TOKEN"].(map[string]interface{})
	if mat["statusCode"] != "404" {
		t.Errorf("expected statusCode 404 for MISSING_AUTHENTICATION_TOKEN, got %v", mat["statusCode"])
	}
}

func TestGenerateSpec_FriendlyErrors(t *testing.T) {
	config := testConfig()
	config.FriendlyErrors = true
	gen := NewOpenAPIGenerator(config)
	spec, _ := gen.GenerateSpec(context.Background(), "ops", testRoutes(), testLambdaARNs(), nil)

	mat := spec.GatewayResponses["MISSING_AUTHENTICATION_TOKEN"].(map[string]interface{})
	templates := mat["responseTemplates"].(map[string]string)
	tmpl := templates["application/json"]
	if !strings.Contains(tmpl, "Route Not Found") {
		t.Error("expected friendly error template for MISSING_AUTHENTICATION_TOKEN")
	}
	if !strings.Contains(tmpl, "dev") {
		t.Error("expected environment in friendly error template")
	}
}

func TestGenerateSpec_ProductionErrors(t *testing.T) {
	config := testConfig()
	config.FriendlyErrors = false
	gen := NewOpenAPIGenerator(config)
	spec, _ := gen.GenerateSpec(context.Background(), "ops", testRoutes(), testLambdaARNs(), nil)

	mat := spec.GatewayResponses["MISSING_AUTHENTICATION_TOKEN"].(map[string]interface{})
	templates := mat["responseTemplates"].(map[string]string)
	tmpl := templates["application/json"]
	if !strings.Contains(tmpl, "Not Found") {
		t.Error("expected production error template")
	}
	if strings.Contains(tmpl, "environment") {
		t.Error("production template should not contain environment")
	}
}

func TestGenerateSpec_WithModels(t *testing.T) {
	models := []utils.ModelDefinition{
		{
			Name:        "CreateItem",
			Description: "Create item request",
			Properties: map[string]utils.ModelProperty{
				"name":  {Type: "string", MaxLength: 100},
				"count": {Type: "integer"},
			},
			Required: []string{"name"},
		},
	}

	routes := []utils.Route{
		{Name: "items_create", Verb: "POST", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito", RequestModel: "CreateItem"},
	}

	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", routes, testLambdaARNs(), models)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.Components == nil || spec.Components.Schemas == nil {
		t.Fatal("expected schemas in components")
	}
	schema, ok := spec.Components.Schemas["CreateItem"]
	if !ok {
		t.Fatal("expected CreateItem schema")
	}
	schemaMap := schema.(map[string]interface{})
	if schemaMap["type"] != "object" {
		t.Errorf("expected object type, got %v", schemaMap["type"])
	}

	// Check title field (required by API Gateway to register as a named model)
	if schemaMap["title"] != "CreateItem" {
		t.Errorf("expected title 'CreateItem', got %v", schemaMap["title"])
	}

	// Check request body reference
	postOp := spec.Paths["/items"]["post"].(map[string]interface{})
	reqBody := postOp["requestBody"].(map[string]interface{})
	content := reqBody["content"].(map[string]interface{})
	jsonContent := content["application/json"].(map[string]interface{})
	schemaRef := jsonContent["schema"].(map[string]string)
	if schemaRef["$ref"] != "#/components/schemas/CreateItem" {
		t.Errorf("expected $ref to CreateItem, got %s", schemaRef["$ref"])
	}

	// Check request validator
	if postOp["x-amazon-apigateway-request-validator"] != "body-only" {
		t.Error("expected body-only request validator")
	}

	// Check definitions section (Swagger 2.0 style for API Gateway model registration)
	if spec.Definitions == nil {
		t.Fatal("expected definitions section for API Gateway model registration")
	}
	defSchema, ok := spec.Definitions["CreateItem"]
	if !ok {
		t.Fatal("expected CreateItem in definitions")
	}
	defSchemaMap := defSchema.(map[string]interface{})
	if defSchemaMap["type"] != "object" {
		t.Errorf("expected object type in definitions, got %v", defSchemaMap["type"])
	}
	if defSchemaMap["title"] != "CreateItem" {
		t.Errorf("expected title 'CreateItem' in definitions, got %v", defSchemaMap["title"])
	}
}

func TestGenerateSpec_WithModels_SnakeCaseToPascalCase(t *testing.T) {
	models := []utils.ModelDefinition{
		{
			Name:       "update_item_customer",
			Properties: map[string]utils.ModelProperty{"name": {Type: "string"}},
		},
	}
	routes := []utils.Route{
		{Name: "items_update", Verb: "PUT", Path: "/items/{id}", Gateway: "customer", Lambda: "customer", Auth: "cognito", RequestModel: "update_item_customer"},
	}

	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", routes, testLambdaARNs(), models)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Schema key must be PascalCase
	if _, ok := spec.Components.Schemas["UpdateItemCustomer"]; !ok {
		t.Errorf("expected PascalCase schema key 'UpdateItemCustomer', got keys: %v", spec.Components.Schemas)
	}

	// Title must also be PascalCase (required by API Gateway for model registration)
	schemaObj := spec.Components.Schemas["UpdateItemCustomer"].(map[string]interface{})
	if schemaObj["title"] != "UpdateItemCustomer" {
		t.Errorf("expected title 'UpdateItemCustomer', got %v", schemaObj["title"])
	}

	// $ref must also be PascalCase
	putOp := spec.Paths["/items/{id}"]["put"].(map[string]interface{})
	reqBody := putOp["requestBody"].(map[string]interface{})
	content := reqBody["content"].(map[string]interface{})
	jsonContent := content["application/json"].(map[string]interface{})
	schemaRef := jsonContent["schema"].(map[string]string)
	if schemaRef["$ref"] != "#/components/schemas/UpdateItemCustomer" {
		t.Errorf("expected PascalCase $ref, got %s", schemaRef["$ref"])
	}
}

func TestGenerateSpec_RequestModel_SkippedForGET(t *testing.T) {
	models := []utils.ModelDefinition{
		{Name: "update_item_customer", Properties: map[string]utils.ModelProperty{"name": {Type: "string"}}},
	}
	routes := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito", RequestModel: "update_item_customer"},
		{Name: "items_update", Verb: "PUT", Path: "/items/{id}", Gateway: "customer", Lambda: "customer", Auth: "cognito", RequestModel: "update_item_customer"},
	}

	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "customer", routes, testLambdaARNs(), models)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// GET should NOT have requestBody or request validator
	getOp := spec.Paths["/items"]["get"].(map[string]interface{})
	if _, ok := getOp["requestBody"]; ok {
		t.Error("GET /items should not have requestBody")
	}
	if _, ok := getOp["x-amazon-apigateway-request-validator"]; ok {
		t.Error("GET /items should not have request validator")
	}

	// PUT should still have requestBody
	putOp := spec.Paths["/items/{id}"]["put"].(map[string]interface{})
	if _, ok := putOp["requestBody"]; !ok {
		t.Error("PUT /items/{id} should have requestBody")
	}
}

func TestGenerateSpec_MissingModel_StillGeneratesSpec(t *testing.T) {
	// When a route references a model that doesn't exist in the models list,
	// the spec should still be generated (with a $ref to the missing model).
	// This ensures terraform apply isn't blocked — the warning is informational.
	routes := []utils.Route{
		{Name: "signup", Verb: "POST", Path: "/signup", Gateway: "onboarding", Lambda: "onboarding", Auth: "none", RequestModel: "signup_form"},
	}

	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "onboarding", routes, testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Spec should still have the $ref even though model is missing
	postOp := spec.Paths["/signup"]["post"].(map[string]interface{})
	reqBody := postOp["requestBody"].(map[string]interface{})
	content := reqBody["content"].(map[string]interface{})
	jsonContent := content["application/json"].(map[string]interface{})
	schemaRef := jsonContent["schema"].(map[string]string)
	if schemaRef["$ref"] != "#/components/schemas/SignupForm" {
		t.Errorf("expected $ref to SignupForm even when model is missing, got %s", schemaRef["$ref"])
	}

	// Components should be nil or have no schemas (model wasn't provided)
	if spec.Components != nil && spec.Components.Schemas != nil {
		if _, ok := spec.Components.Schemas["SignupForm"]; ok {
			t.Error("did not expect SignupForm schema when model was not provided")
		}
	}
}

func TestGenerateSpec_NoRoutes(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	_, err := gen.GenerateSpec(context.Background(), "nonexistent", testRoutes(), testLambdaARNs(), nil)
	if err == nil {
		t.Error("expected error for gateway with no routes")
	}
}

func TestGenerateSpec_MultipleFrontendUrls(t *testing.T) {
	config := testConfig()
	config.FrontendUrls = []string{"https://app.example.com", "https://admin.example.com"}
	gen := NewOpenAPIGenerator(config)
	spec, _ := gen.GenerateSpec(context.Background(), "ops", testRoutes(), testLambdaARNs(), nil)

	// Multiple URLs → wildcard
	healthPath := spec.Paths["/health"]
	optionsOp := healthPath["options"].(map[string]interface{})
	integration := optionsOp["x-amazon-apigateway-integration"].(map[string]interface{})
	responses := integration["responses"].(map[string]interface{})
	defaultResp := responses["default"].(map[string]interface{})
	respParams := defaultResp["responseParameters"].(map[string]string)
	if respParams["method.response.header.Access-Control-Allow-Origin"] != "'*'" {
		t.Errorf("expected wildcard origin for multiple frontend URLs, got %s",
			respParams["method.response.header.Access-Control-Allow-Origin"])
	}
}

func TestGenerateSpecJSON_ValidJSON(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	data, err := gen.GenerateSpecJSON(context.Background(), "customer", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated spec is not valid JSON: %v", err)
	}

	if parsed["openapi"] != "3.0.1" {
		t.Errorf("expected openapi 3.0.1 in JSON output")
	}
}

func TestCalculateSpecHash_Deterministic(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	ctx := context.Background()
	routes := testRoutes()
	arns := testLambdaARNs()

	hash1, err := gen.CalculateSpecHash(ctx, "customer", routes, arns, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hash2, err := gen.CalculateSpecHash(ctx, "customer", routes, arns, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("expected deterministic hash, got %s and %s", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("expected 64-char SHA-256 hex, got %d chars", len(hash1))
	}
}

func TestCalculateSpecHash_ChangesOnRouteChange(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	ctx := context.Background()
	arns := testLambdaARNs()

	routes1 := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
	}
	routes2 := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
		{Name: "items_create", Verb: "POST", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito"},
	}

	hash1, _ := gen.CalculateSpecHash(ctx, "customer", routes1, arns, nil)
	hash2, _ := gen.CalculateSpecHash(ctx, "customer", routes2, arns, nil)

	if hash1 == hash2 {
		t.Error("expected different hashes for different routes")
	}
}

func TestGenerateAllSpecs(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	specs, err := gen.GenerateAllSpecs(context.Background(), testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(specs) != 3 {
		t.Errorf("expected 3 gateway specs, got %d", len(specs))
	}

	for _, gw := range []string{"customer", "ops", "onboarding"} {
		if _, ok := specs[gw]; !ok {
			t.Errorf("expected spec for gateway %s", gw)
		}
	}
}

func TestExtractPathParameters(t *testing.T) {
	tests := []struct {
		path     string
		expected []string
	}{
		{"/items", nil},
		{"/items/{id}", []string{"id"}},
		{"/items/{id}/details/{detailId}", []string{"id", "detailId"}},
		{"/", nil},
		{"/{proxy+}", []string{"proxy+"}},
	}

	for _, tt := range tests {
		params := extractPathParameters(tt.path)
		if len(params) != len(tt.expected) {
			t.Errorf("path %s: expected %d params, got %d", tt.path, len(tt.expected), len(params))
			continue
		}
		for i, p := range params {
			if p != tt.expected[i] {
				t.Errorf("path %s: param %d expected %s, got %s", tt.path, i, tt.expected[i], p)
			}
		}
	}
}

func TestGenerateSpec_LambdaARNFallback(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	// Pass empty ARN map — should construct ARN from config
	spec, err := gen.GenerateSpec(context.Background(), "ops", testRoutes(), map[string]string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	healthPath := spec.Paths["/health"]
	getOp := healthPath["get"].(map[string]interface{})
	integration := getOp["x-amazon-apigateway-integration"].(map[string]interface{})
	uri := integration["uri"].(string)
	if !strings.Contains(uri, "myapp-dev-ops") {
		t.Errorf("expected fallback ARN with myapp-dev-ops, got %s", uri)
	}
}

func TestGenerateSpec_IAMAuth(t *testing.T) {
	routes := []utils.Route{
		{Name: "internal_sync", Verb: "POST", Path: "/sync", Gateway: "internal", Lambda: "internal", Auth: "iam"},
	}
	arns := map[string]string{"internal": "arn:aws:lambda:us-east-1:123456789012:function:myapp-dev-internal"}

	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "internal", routes, arns, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	postOp := spec.Paths["/sync"]["post"].(map[string]interface{})
	security := postOp["security"].([]map[string]interface{})
	if _, ok := security[0]["sigv4"]; !ok {
		t.Error("expected sigv4 security for IAM auth")
	}
}

// Property-based test: every generated spec is valid JSON and has required fields
func TestGenerateSpec_Property_AlwaysValidJSON(t *testing.T) {
	r := rand.New(rand.NewSource(42))

	for i := 0; i < 50; i++ {
		config := testConfig()
		config.FriendlyErrors = r.Intn(2) == 0
		if r.Intn(3) == 0 {
			config.FrontendUrls = []string{"https://a.com", "https://b.com"}
		}

		routes, arns, gw := generateRandomGatewayRoutes(r)
		gen := NewOpenAPIGenerator(config)
		data, err := gen.GenerateSpecJSON(context.Background(), gw, routes, arns, nil)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("iteration %d: invalid JSON: %v", i, err)
		}

		if parsed["openapi"] != "3.0.1" {
			t.Errorf("iteration %d: missing openapi version", i)
		}
		if _, ok := parsed["paths"]; !ok {
			t.Errorf("iteration %d: missing paths", i)
		}
	}
}

// Property: every path has an OPTIONS method
func TestGenerateSpec_Property_AllPathsHaveOptions(t *testing.T) {
	r := rand.New(rand.NewSource(99))

	for i := 0; i < 30; i++ {
		routes, arns, gw := generateRandomGatewayRoutes(r)
		gen := NewOpenAPIGenerator(testConfig())
		spec, err := gen.GenerateSpec(context.Background(), gw, routes, arns, nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}

		for path, methods := range spec.Paths {
			if _, ok := methods["options"]; !ok {
				t.Errorf("iteration %d: path %s missing OPTIONS", i, path)
			}
		}
	}
}

// Property: spec hash is deterministic across multiple calls
func TestGenerateSpec_Property_HashDeterminism(t *testing.T) {
	r := rand.New(rand.NewSource(77))

	for i := 0; i < 20; i++ {
		routes, arns, gw := generateRandomGatewayRoutes(r)
		gen := NewOpenAPIGenerator(testConfig())
		ctx := context.Background()

		h1, _ := gen.CalculateSpecHash(ctx, gw, routes, arns, nil)
		h2, _ := gen.CalculateSpecHash(ctx, gw, routes, arns, nil)
		if h1 != h2 {
			t.Errorf("iteration %d: non-deterministic hash for gateway %s", i, gw)
		}
	}
}

func generateRandomGatewayRoutes(r *rand.Rand) ([]utils.Route, map[string]string, string) {
	gw := randomOpenAPIString(r, 3, 8)
	lambda := randomOpenAPIString(r, 3, 8)
	count := r.Intn(10) + 1

	verbs := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	auths := []string{"none", "cognito"}

	routes := make([]utils.Route, 0, count)
	usedPaths := make(map[string]map[string]bool)

	for j := 0; j < count; j++ {
		path := "/" + randomOpenAPIString(r, 2, 6)
		if r.Intn(3) == 0 {
			path += "/{id}"
		}
		verb := verbs[r.Intn(len(verbs))]

		// Avoid duplicate verb+path
		if usedPaths[path] == nil {
			usedPaths[path] = make(map[string]bool)
		}
		if usedPaths[path][verb] {
			continue
		}
		usedPaths[path][verb] = true

		routes = append(routes, utils.Route{
			Name:    randomOpenAPIString(r, 3, 10),
			Verb:    verb,
			Path:    path,
			Gateway: gw,
			Lambda:  lambda,
			Auth:    auths[r.Intn(len(auths))],
		})
	}

	arns := map[string]string{
		lambda: "arn:aws:lambda:us-east-1:123456789012:function:test-" + lambda,
	}

	return routes, arns, gw
}

func randomOpenAPIString(r *rand.Rand, minLen, maxLen int) string {
	length := minLen + r.Intn(maxLen-minLen+1)
	chars := "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, length)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return string(b)
}

// Test that paths are sorted in the output for determinism
func TestGenerateSpec_PathsSorted(t *testing.T) {
	gen := NewOpenAPIGenerator(testConfig())
	data, _ := gen.GenerateSpecJSON(context.Background(), "customer", testRoutes(), testLambdaARNs(), nil)

	// Parse and check path order in JSON
	var parsed map[string]json.RawMessage
	json.Unmarshal(data, &parsed)

	var pathsMap map[string]json.RawMessage
	json.Unmarshal(parsed["paths"], &pathsMap)

	paths := make([]string, 0, len(pathsMap))
	for p := range pathsMap {
		paths = append(paths, p)
	}

	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.Strings(sorted)

	for i := range paths {
		if paths[i] != sorted[i] {
			t.Errorf("paths not sorted: position %d has %s, expected %s", i, paths[i], sorted[i])
		}
	}
}

func TestGenerateSpec_MixedAuthOnSameGateway(t *testing.T) {
	// ops gateway has both cognito and none auth routes
	gen := NewOpenAPIGenerator(testConfig())
	spec, err := gen.GenerateSpec(context.Background(), "ops", testRoutes(), testLambdaARNs(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// /health is auth=none
	healthGet := spec.Paths["/health"]["get"].(map[string]interface{})
	if _, ok := healthGet["security"]; ok {
		t.Error("/health should have no security")
	}

	// /containers is auth=cognito
	containersGet := spec.Paths["/containers"]["get"].(map[string]interface{})
	if _, ok := containersGet["security"]; !ok {
		t.Error("/containers should have security")
	}

	// Should have cognito security scheme since at least one route uses it
	if spec.Components == nil || spec.Components.SecuritySchemes == nil {
		t.Error("expected security schemes when gateway has cognito routes")
	}
}
