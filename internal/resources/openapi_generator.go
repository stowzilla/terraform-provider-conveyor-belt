package resources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"terraform-provider-conveyor-belt/internal/utils"
)

// OpenAPISpec represents a complete OpenAPI 3.0 specification for one API Gateway.
type OpenAPISpec struct {
	OpenAPI    string                            `json:"openapi"`
	Info       OpenAPIInfo                       `json:"info"`
	Paths      map[string]map[string]interface{} `json:"paths"`
	Components *OpenAPIComponents                `json:"components,omitempty"`

	// Swagger 2.0-style definitions — API Gateway REST APIs use this section to create
	// named Model resources during import. Without definitions, models referenced via
	// $ref in components/schemas may not be registered on first-time gateway creation.
	Definitions map[string]interface{} `json:"definitions,omitempty"`

	// x-amazon-apigateway extensions at the top level
	GatewayResponses    map[string]interface{} `json:"x-amazon-apigateway-gateway-responses,omitempty"`
	RequestValidators   map[string]interface{} `json:"x-amazon-apigateway-request-validators,omitempty"`
}

// OpenAPIInfo is the info block of the spec.
type OpenAPIInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

// OpenAPIComponents holds reusable components (securitySchemes, schemas).
type OpenAPIComponents struct {
	SecuritySchemes map[string]interface{} `json:"securitySchemes,omitempty"`
	Schemas         map[string]interface{} `json:"schemas,omitempty"`
}

// OpenAPIGenerator builds OpenAPI specs from routes and config.
type OpenAPIGenerator struct {
	config *DispatcherConfig
}

// NewOpenAPIGenerator creates a generator for the given config.
func NewOpenAPIGenerator(config *DispatcherConfig) *OpenAPIGenerator {
	return &OpenAPIGenerator{config: config}
}

// GenerateSpec produces an OpenAPI 3.0 spec for a single gateway.
// lambdaARNs maps lambda name → Lambda function ARN.
func (g *OpenAPIGenerator) GenerateSpec(
	ctx context.Context,
	gatewayName string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	models []utils.ModelDefinition,
) (*OpenAPISpec, error) {
	gatewayRoutes := filterRoutesByGateway(routes, gatewayName)
	if len(gatewayRoutes) == 0 {
		return nil, fmt.Errorf("no routes for gateway %q", gatewayName)
	}

	apiName := fmt.Sprintf("%s-%s-%s", g.config.AppName, g.config.Environment, gatewayName)

	spec := &OpenAPISpec{
		OpenAPI: "3.0.1",
		Info: OpenAPIInfo{
			Title:   apiName,
			Version: "1.0",
		},
		Paths: make(map[string]map[string]interface{}),
	}

	// Build components
	spec.Components = g.buildComponents(gatewayRoutes, models, gatewayName)

	// Warn about routes that reference models not found in the schema definitions
	availableSchemas := make(map[string]bool)
	if spec.Components != nil && spec.Components.Schemas != nil {
		for name := range spec.Components.Schemas {
			availableSchemas[name] = true
		}
	}
	for _, r := range gatewayRoutes {
		if r.RequestModel != "" {
			verb := strings.ToUpper(r.Verb)
			if verb == "POST" || verb == "PUT" || verb == "PATCH" {
				pascalName := snakeToPascal(r.RequestModel)
				if !availableSchemas[pascalName] {
					utils.Warn(ctx, "Route references request_model not found in schema definitions — API Gateway may reject this spec on first deploy", map[string]interface{}{
						"gateway":         gatewayName,
						"route":           r.Path,
						"verb":            r.Verb,
						"request_model":   r.RequestModel,
						"expected_schema": pascalName,
						"models_count":    len(models),
						"schemas_count":   len(availableSchemas),
					})
				}
			}
		}
	}

	// Build paths
	g.buildPaths(ctx, spec, gatewayRoutes, lambdaARNs)

	// Build gateway responses
	spec.GatewayResponses = g.buildGatewayResponses()

	// Add request validators if any route uses request models
	hasRequestModels := false
	for _, r := range gatewayRoutes {
		if r.RequestModel != "" {
			hasRequestModels = true
			break
		}
	}
	if hasRequestModels {
		spec.RequestValidators = map[string]interface{}{
			"body-only": map[string]interface{}{
				"validateRequestBody":       true,
				"validateRequestParameters": false,
			},
		}
	}

	// Populate definitions (Swagger 2.0 style) for API Gateway model registration.
	// API Gateway REST APIs create named Model resources from the "definitions" section
	// during OpenAPI import. Without this, models may not be registered on first deploy
	// even if they exist in components/schemas.
	if spec.Components != nil && len(spec.Components.Schemas) > 0 {
		spec.Definitions = make(map[string]interface{}, len(spec.Components.Schemas))
		for name, schema := range spec.Components.Schemas {
			spec.Definitions[name] = schema
		}
	}

	return spec, nil
}

// GenerateSpecJSON returns the spec as indented JSON bytes.
func (g *OpenAPIGenerator) GenerateSpecJSON(
	ctx context.Context,
	gatewayName string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	models []utils.ModelDefinition,
) ([]byte, error) {
	spec, err := g.GenerateSpec(ctx, gatewayName, routes, lambdaARNs, models)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(spec, "", "  ")
}

// CalculateSpecHash returns a SHA-256 hash of the generated spec JSON.
func (g *OpenAPIGenerator) CalculateSpecHash(
	ctx context.Context,
	gatewayName string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	models []utils.ModelDefinition,
) (string, error) {
	data, err := g.GenerateSpecJSON(ctx, gatewayName, routes, lambdaARNs, models)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

// buildComponents creates securitySchemes and schemas.
func (g *OpenAPIGenerator) buildComponents(
	routes []utils.Route,
	models []utils.ModelDefinition,
	gatewayName string,
) *OpenAPIComponents {
	components := &OpenAPIComponents{}

	// Add Cognito authorizer if any route uses cognito auth
	hasCognito := false
	for _, r := range routes {
		if r.Auth == "cognito" {
			hasCognito = true
			break
		}
	}
	if hasCognito && len(g.config.CognitoUserPoolArns) > 0 {
		components.SecuritySchemes = map[string]interface{}{
			"CognitoUserPoolAuthorizer": map[string]interface{}{
				"type": "apiKey",
				"name": "Authorization",
				"in":   "header",
				"x-amazon-apigateway-authtype": "cognito_user_pools",
				"x-amazon-apigateway-authorizer": map[string]interface{}{
					"type":         "cognito_user_pools",
					"providerARNs": g.config.CognitoUserPoolArns,
				},
			},
		}
	}

	// Add model schemas — collect models needed by routes on this gateway
	gatewayModels := GetModelsForGateway(routes, models, gatewayName)
	if len(gatewayModels) > 0 {
		schemas := make(map[string]interface{})
		for _, m := range gatewayModels {
			schemas[snakeToPascal(m.Name)] = g.modelToOpenAPISchema(m)
		}
		components.Schemas = schemas
	}

	// Fallback: if routes reference models not found via GetModelsForGateway,
	// try matching by PascalCase name (handles case where model.Name is already PascalCase)
	if components.Schemas == nil {
		components.Schemas = make(map[string]interface{})
	}
	modelsByPascal := make(map[string]utils.ModelDefinition)
	for _, m := range models {
		modelsByPascal[snakeToPascal(m.Name)] = m
		// Also index by raw name in case it's already PascalCase
		modelsByPascal[m.Name] = m
	}
	for _, r := range routes {
		if r.RequestModel == "" {
			continue
		}
		pascalName := snakeToPascal(r.RequestModel)
		if _, exists := components.Schemas[pascalName]; exists {
			continue // Already included
		}
		// Try to find by PascalCase or raw name
		if m, ok := modelsByPascal[pascalName]; ok {
			components.Schemas[pascalName] = g.modelToOpenAPISchema(m)
		} else if m, ok := modelsByPascal[r.RequestModel]; ok {
			components.Schemas[pascalName] = g.modelToOpenAPISchema(m)
		}
	}
	// Clean up empty schemas map
	if len(components.Schemas) == 0 {
		components.Schemas = nil
	}

	if components.SecuritySchemes == nil && components.Schemas == nil {
		return nil
	}
	return components
}

// buildPaths populates spec.Paths from routes.
func (g *OpenAPIGenerator) buildPaths(
	ctx context.Context,
	spec *OpenAPISpec,
	routes []utils.Route,
	lambdaARNs map[string]string,
) {
	// Group routes by path
	pathRoutes := make(map[string][]utils.Route)
	for _, r := range routes {
		pathRoutes[r.Path] = append(pathRoutes[r.Path], r)
	}

	// Sort paths for deterministic output
	paths := make([]string, 0, len(pathRoutes))
	for p := range pathRoutes {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		routesForPath := pathRoutes[path]
		pathItem := make(map[string]interface{})

		// Add each HTTP method
		for _, route := range routesForPath {
			method := strings.ToLower(route.Verb)
			pathItem[method] = g.buildOperation(ctx, route, lambdaARNs)
		}

		// Add OPTIONS for CORS
		pathItem["options"] = g.buildCorsOptions(routesForPath, path)

		spec.Paths[path] = pathItem
	}
}

// buildOperation creates the operation object for a single route.
func (g *OpenAPIGenerator) buildOperation(
	ctx context.Context,
	route utils.Route,
	lambdaARNs map[string]string,
) map[string]interface{} {
	op := map[string]interface{}{
		"operationId": fmt.Sprintf("%s_%s", route.Name, strings.ToLower(route.Verb)),
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Success",
				"headers": map[string]interface{}{
					"Access-Control-Allow-Origin": map[string]interface{}{
						"schema": map[string]string{"type": "string"},
					},
				},
			},
		},
	}

	// Security
	switch route.Auth {
	case "cognito":
		op["security"] = []map[string]interface{}{
			{"CognitoUserPoolAuthorizer": []string{}},
		}
	case "iam":
		op["security"] = []map[string]interface{}{
			{"sigv4": []string{}},
		}
	}

	// Path parameters
	params := extractPathParameters(route.Path)
	if len(params) > 0 {
		paramList := make([]map[string]interface{}, 0, len(params))
		for _, p := range params {
			paramList = append(paramList, map[string]interface{}{
				"name":     p,
				"in":       "path",
				"required": true,
				"schema":   map[string]string{"type": "string"},
			})
		}
		op["parameters"] = paramList
	}

	// Request model validation — only for methods that accept a body
	verb := strings.ToUpper(route.Verb)
	if route.RequestModel != "" && (verb == "POST" || verb == "PUT" || verb == "PATCH") {
		op["requestBody"] = map[string]interface{}{
			"required": true,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]string{
						"$ref": "#/components/schemas/" + snakeToPascal(route.RequestModel),
					},
				},
			},
		}
		op["x-amazon-apigateway-request-validator"] = "body-only"
	}

	// Lambda integration
	lambdaArn := lambdaARNs[route.Lambda]
	if lambdaArn == "" {
		// Fallback: construct ARN
		functionName := fmt.Sprintf("%s-%s-%s", g.config.AppName, g.config.Environment, route.Lambda)
		lambdaArn = fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", g.config.AwsRegion, g.config.AwsAccountId, functionName)
		utils.Warn(ctx, "Lambda ARN not found, using constructed ARN for OpenAPI spec", map[string]interface{}{
			"lambda": route.Lambda,
		})
	}

	integrationUri := fmt.Sprintf("arn:aws:apigateway:%s:lambda:path/2015-03-31/functions/%s/invocations",
		g.config.AwsRegion, lambdaArn)

	op["x-amazon-apigateway-integration"] = map[string]interface{}{
		"type":                  "aws_proxy",
		"httpMethod":            "POST",
		"uri":                   integrationUri,
		"passthroughBehavior":   "when_no_match",
		"contentHandling":       "CONVERT_TO_TEXT",
	}

	return op
}

// buildCorsOptions creates the OPTIONS method for CORS preflight.
func (g *OpenAPIGenerator) buildCorsOptions(routes []utils.Route, path string) map[string]interface{} {
	// Compute allowed methods
	methodSet := make(map[string]bool)
	for _, r := range routes {
		if r.Path == path {
			methodSet[r.Verb] = true
		}
	}
	methodSet["OPTIONS"] = true
	sorted := make([]string, 0, len(methodSet))
	for m := range methodSet {
		sorted = append(sorted, m)
	}
	sort.Strings(sorted)
	allowMethods := strings.Join(sorted, ",")

	frontendUrl := GetCORSOriginForConfig(g.config)

	return map[string]interface{}{
		"summary": "CORS preflight",
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "CORS preflight response",
				"headers": map[string]interface{}{
					"Access-Control-Allow-Origin": map[string]interface{}{
						"schema": map[string]string{"type": "string"},
					},
					"Access-Control-Allow-Methods": map[string]interface{}{
						"schema": map[string]string{"type": "string"},
					},
					"Access-Control-Allow-Headers": map[string]interface{}{
						"schema": map[string]string{"type": "string"},
					},
					"Access-Control-Max-Age": map[string]interface{}{
						"schema": map[string]string{"type": "string"},
					},
				},
			},
		},
		"x-amazon-apigateway-integration": map[string]interface{}{
			"type": "mock",
			"requestTemplates": map[string]string{
				"application/json": `{"statusCode": 200}`,
			},
			"responses": map[string]interface{}{
				"default": map[string]interface{}{
					"statusCode": "200",
					"responseParameters": map[string]string{
						"method.response.header.Access-Control-Allow-Origin":  "'" + frontendUrl + "'",
						"method.response.header.Access-Control-Allow-Methods": "'" + allowMethods + "'",
						"method.response.header.Access-Control-Allow-Headers": "'Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token'",
						"method.response.header.Access-Control-Max-Age":       "'86400'",
					},
					"responseTemplates": map[string]string{
						"application/json": "{}",
					},
				},
			},
		},
	}
}

// buildGatewayResponses creates x-amazon-apigateway-gateway-responses.
func (g *OpenAPIGenerator) buildGatewayResponses() map[string]interface{} {
	frontendUrl := GetCORSOriginForConfig(g.config)

	responseParams := map[string]string{
		"gatewayresponse.header.Access-Control-Allow-Origin":  "'" + frontendUrl + "'",
		"gatewayresponse.header.Access-Control-Allow-Headers": "'Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token'",
		"gatewayresponse.header.Access-Control-Allow-Methods": "'GET,POST,PUT,DELETE,PATCH,OPTIONS'",
	}

	type gwResponseDef struct {
		key        string
		statusCode string
		template   string
	}

	defs := []gwResponseDef{
		{key: "UNAUTHORIZED", template: g.gatewayResponseTemplate("UNAUTHORIZED")},
		{key: "ACCESS_DENIED", template: g.gatewayResponseTemplate("ACCESS_DENIED")},
		{key: "DEFAULT_4XX", template: g.gatewayResponseTemplate("DEFAULT_4XX")},
		{key: "DEFAULT_5XX", template: g.gatewayResponseTemplate("DEFAULT_5XX")},
		{key: "MISSING_AUTHENTICATION_TOKEN", statusCode: "404", template: g.gatewayResponseTemplate("MISSING_AUTHENTICATION_TOKEN")},
		{key: "RESOURCE_NOT_FOUND", template: g.gatewayResponseTemplate("RESOURCE_NOT_FOUND")},
		{key: "BAD_REQUEST_BODY", template: g.gatewayResponseTemplate("BAD_REQUEST_BODY")},
	}

	responses := make(map[string]interface{})
	for _, d := range defs {
		entry := map[string]interface{}{
			"responseParameters": responseParams,
		}
		if d.template != "" {
			entry["responseTemplates"] = map[string]string{
				"application/json": d.template,
			}
		}
		if d.statusCode != "" {
			entry["statusCode"] = d.statusCode
		}
		responses[d.key] = entry
	}
	return responses
}

// gatewayResponseTemplate returns the VTL template for a gateway response type.
func (g *OpenAPIGenerator) gatewayResponseTemplate(responseType string) string {
	if g.config.FriendlyErrors {
		return g.friendlyTemplate(responseType)
	}
	return g.productionTemplate(responseType)
}

func (g *OpenAPIGenerator) friendlyTemplate(responseType string) string {
	env := g.config.Environment
	switch responseType {
	case "MISSING_AUTHENTICATION_TOKEN":
		return fmt.Sprintf(`{"error":"Route Not Found","message":"The API route you are trying to access does not exist. Check the HTTP method (GET/POST/PUT/DELETE) and path.","path":"$context.resourcePath","method":"$context.httpMethod","environment":"%s","hint":"Run ./scripts/list_routes.rb to see all available routes"}`, env)
	case "RESOURCE_NOT_FOUND":
		return fmt.Sprintf(`{"error":"Method Not Allowed","message":"The HTTP method is not allowed for this resource.","path":"$context.resourcePath","method":"$context.httpMethod","environment":"%s","hint":"This route exists but doesn't support $context.httpMethod. Try a different HTTP method (GET, POST, PUT, DELETE, or OPTIONS)."}`, env)
	case "BAD_REQUEST_BODY":
		return fmt.Sprintf(`{"error":"Bad Request","message":"The request body is invalid or malformed JSON.","details":$context.error.messageString,"environment":"%s"}`, env)
	case "UNAUTHORIZED":
		return fmt.Sprintf(`{"error":"Unauthorized","message":"Authentication failed. Check your authorization token.","details":$context.error.messageString,"environment":"%s"}`, env)
	case "ACCESS_DENIED":
		return fmt.Sprintf(`{"error":"Access Denied","message":"You don't have permission to access this resource.","details":$context.error.messageString,"environment":"%s"}`, env)
	case "DEFAULT_4XX":
		return fmt.Sprintf(`{"error":"Client Error","message":"The request could not be processed. This may be due to an unsupported HTTP method, invalid headers, or malformed request.","path":"$context.resourcePath","method":"$context.httpMethod","environment":"%s","hint":"Check that you're using the correct HTTP method (GET, POST, PUT, DELETE) for this route. If the route exists, try a different method."}`, env)
	default:
		return fmt.Sprintf(`{"error":"$context.error.responseType","message":$context.error.messageString,"environment":"%s"}`, env)
	}
}

func (g *OpenAPIGenerator) productionTemplate(responseType string) string {
	switch responseType {
	case "MISSING_AUTHENTICATION_TOKEN", "RESOURCE_NOT_FOUND":
		return `{"error":"Not Found","message":"The requested resource does not exist"}`
	case "BAD_REQUEST_BODY":
		return `{"error":"Bad Request","message":"Invalid request body"}`
	case "UNAUTHORIZED":
		return `{"error":"Unauthorized","message":"Authentication required"}`
	case "ACCESS_DENIED":
		return `{"error":"Forbidden","message":"Access denied"}`
	case "DEFAULT_4XX":
		return `{"error":"Bad Request","message":"The request could not be processed"}`
	default:
		return `{"error":"Error","message":"An error occurred processing your request"}`
	}
}

// modelToOpenAPISchema converts a ModelDefinition to an OpenAPI schema object.
func (g *OpenAPIGenerator) modelToOpenAPISchema(model utils.ModelDefinition) map[string]interface{} {
	schema := map[string]interface{}{
		"type":  "object",
		"title": snakeToPascal(model.Name),
	}
	if model.Description != "" {
		schema["description"] = model.Description
	}
	if len(model.Properties) > 0 {
		schema["properties"] = propertiesToSchema(model.Properties)
	}
	if len(model.Required) > 0 {
		sorted := make([]string, len(model.Required))
		copy(sorted, model.Required)
		sort.Strings(sorted)
		schema["required"] = sorted
	}
	return schema
}

// extractPathParameters extracts parameter names from a path like /items/{id}/details/{detailId}.
func extractPathParameters(path string) []string {
	var params []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			params = append(params, seg[1:len(seg)-1])
		}
	}
	return params
}

// GenerateAllSpecs generates specs for all gateways found in routes.
func (g *OpenAPIGenerator) GenerateAllSpecs(
	ctx context.Context,
	routes []utils.Route,
	lambdaARNs map[string]string,
	models []utils.ModelDefinition,
) (map[string]*OpenAPISpec, error) {
	// Discover unique gateways
	gatewaySet := make(map[string]bool)
	for _, r := range routes {
		if r.Gateway != "" {
			gatewaySet[r.Gateway] = true
		}
	}

	specs := make(map[string]*OpenAPISpec)
	for gw := range gatewaySet {
		spec, err := g.GenerateSpec(ctx, gw, routes, lambdaARNs, models)
		if err != nil {
			return nil, fmt.Errorf("failed to generate spec for gateway %q: %w", gw, err)
		}
		specs[gw] = spec
	}
	return specs, nil
}
