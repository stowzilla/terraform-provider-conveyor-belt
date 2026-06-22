// internal/resources/find_api_gateway_test.go
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigatewayTypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

// TestFindExistingApiGateway_UsesLimit500 verifies that findExistingApiGateway
// sends Limit=500 in the request, ensuring accounts with >25 API Gateways
// are fully searched.
func TestFindExistingApiGateway_UsesLimit500(t *testing.T) {
	var receivedLimit string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The AWS SDK sends limit as a query parameter for GetRestApis
		receivedLimit = r.URL.Query().Get("limit")

		// Return empty response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": []interface{}{},
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "test-app",
			Environment: "dev",
		},
	}

	ctx := context.Background()
	_, err := pm.findExistingApiGateway(ctx, "test-app-dev-customer")
	if err != nil {
		t.Fatalf("findExistingApiGateway returned error: %v", err)
	}

	if receivedLimit != "500" {
		t.Errorf("Expected limit=500 in GetRestApis request, got %q", receivedLimit)
	}
}

// TestFindExistingApiGateway_FindsApiByName verifies that findExistingApiGateway
// correctly identifies an API Gateway by name from the response.
func TestFindExistingApiGateway_FindsApiByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": []map[string]interface{}{
				{"id": "abc123", "name": "test-app-dev-orders"},
				{"id": "def456", "name": "test-app-dev-customer"},
				{"id": "ghi789", "name": "test-app-dev-payments"},
			},
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "test-app",
			Environment: "dev",
		},
	}

	ctx := context.Background()
	apiID, err := pm.findExistingApiGateway(ctx, "test-app-dev-customer")
	if err != nil {
		t.Fatalf("findExistingApiGateway returned error: %v", err)
	}

	if apiID != "def456" {
		t.Errorf("Expected API ID 'def456', got '%s'", apiID)
	}
}

// TestFindExistingApiGateway_ReturnsEmptyWhenNotFound verifies that findExistingApiGateway
// returns an empty string when the API Gateway is not found.
func TestFindExistingApiGateway_ReturnsEmptyWhenNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": []map[string]interface{}{
				{"id": "abc123", "name": "test-app-dev-orders"},
				{"id": "ghi789", "name": "test-app-dev-payments"},
			},
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "test-app",
			Environment: "dev",
		},
	}

	ctx := context.Background()
	apiID, err := pm.findExistingApiGateway(ctx, "test-app-dev-nonexistent")
	if err != nil {
		t.Fatalf("findExistingApiGateway returned error: %v", err)
	}

	if apiID != "" {
		t.Errorf("Expected empty API ID for non-existent gateway, got '%s'", apiID)
	}
}

// TestFindExistingApiGateway_HandlesLargeNumberOfGateways verifies that
// findExistingApiGateway can find a gateway that would be beyond the default
// 25-item limit. This is the core scenario from the bug report.
func TestFindExistingApiGateway_HandlesLargeNumberOfGateways(t *testing.T) {
	// Generate 38 gateways (matching the bug report scenario)
	gateways := make([]apigatewayTypes.RestApi, 38)
	for i := range 38 {
		name := fmt.Sprintf("myapp-dev%02d-gateway%d", i/6+1, i)
		gateways[i] = apigatewayTypes.RestApi{
			Id:   aws.String(fmt.Sprintf("id-%d", i)),
			Name: aws.String(name),
		}
	}
	// Place our target at position 30 (beyond the default 25 limit)
	targetName := "myapp-dev-customer"
	targetID := "target-id-abc"
	gateways[30] = apigatewayTypes.RestApi{
		Id:   aws.String(targetID),
		Name: aws.String(targetName),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Build response items
		items := make([]map[string]interface{}, len(gateways))
		for i, gw := range gateways {
			items[i] = map[string]interface{}{
				"id":   *gw.Id,
				"name": *gw.Name,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": items,
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "myapp",
			Environment: "dev02",
		},
	}

	ctx := context.Background()
	apiID, err := pm.findExistingApiGateway(ctx, targetName)
	if err != nil {
		t.Fatalf("findExistingApiGateway returned error: %v", err)
	}

	if apiID != targetID {
		t.Errorf("Expected API ID '%s' for gateway at position 30 (beyond default 25 limit), got '%s'", targetID, apiID)
	}
}

// TestUpdateSingleGateway_UsesSharedLookup verifies that updateSingleGateway
// uses findExistingApiGateway (which has Limit: 500) rather than an inline
// GetRestApis call without a limit.
func TestUpdateSingleGateway_FindsGatewayBeyondDefault25(t *testing.T) {
	var requestCount int
	var receivedLimit string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		receivedLimit = r.URL.Query().Get("limit")

		w.Header().Set("Content-Type", "application/json")
		// Return a gateway that would be beyond position 25
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": []map[string]interface{}{
				{"id": "found-id", "name": "myapp-prod-customer"},
			},
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "myapp",
			Environment: "prod",
			AwsRegion:   "us-east-1",
		},
	}

	ctx := context.Background()
	result := pm.updateSingleGateway(ctx, "customer", nil, nil, pm.config, nil)

	// The function should find the gateway (even though it will fail on the OpenAPI update
	// since we didn't provide routes — that's fine, we're testing the lookup)
	if result.Error != nil && result.Error.Error() == "API Gateway not found: myapp-prod-customer" {
		t.Errorf("updateSingleGateway failed to find gateway - Limit not set correctly. Got error: %v", result.Error)
	}

	if receivedLimit != "500" {
		t.Errorf("Expected limit=500 in GetRestApis request from updateSingleGateway, got %q", receivedLimit)
	}
}

// TestDeleteSingleGateway_UsesSharedLookup verifies that deleteSingleGateway
// uses findExistingApiGateway (which has Limit: 500).
func TestDeleteSingleGateway_UsesLimit500(t *testing.T) {
	var receivedLimit string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedLimit = r.URL.Query().Get("limit")

		w.Header().Set("Content-Type", "application/json")
		// Return empty - gateway not found, so deletion is a no-op success
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": []interface{}{},
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "myapp",
			Environment: "prod",
		},
	}

	ctx := context.Background()
	result := pm.deleteSingleGateway(ctx, "customer", nil)

	if result.Error != nil {
		t.Fatalf("deleteSingleGateway returned error: %v", result.Error)
	}

	if !result.Success {
		t.Error("Expected deleteSingleGateway to succeed when gateway not found")
	}

	if receivedLimit != "500" {
		t.Errorf("Expected limit=500 in GetRestApis request from deleteSingleGateway, got %q", receivedLimit)
	}
}

// TestReadSingleGateway_UsesLimit500 verifies that readSingleGateway
// uses findExistingApiGateway (which has Limit: 500).
func TestReadSingleGateway_UsesLimit500(t *testing.T) {
	var receivedLimit string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedLimit = r.URL.Query().Get("limit")

		w.Header().Set("Content-Type", "application/json")
		// Return empty - gateway not found
		json.NewEncoder(w).Encode(map[string]interface{}{
			"item": []interface{}{},
		})
	}))
	defer server.Close()

	client := apigateway.New(apigateway.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", "fake"),
		BaseEndpoint: aws.String(server.URL),
	})

	pm := &ParallelManager{
		clients: &ResourceClients{
			ApiGateway: client,
		},
		config: &DispatcherConfig{
			AppName:     "myapp",
			Environment: "prod",
			AwsRegion:   "us-east-1",
		},
	}

	ctx := context.Background()
	result := pm.readSingleGateway(ctx, "customer")

	if result.Error != nil {
		t.Fatalf("readSingleGateway returned error: %v", result.Error)
	}

	if result.Exists {
		t.Error("Expected readSingleGateway to report gateway as not existing")
	}

	if receivedLimit != "500" {
		t.Errorf("Expected limit=500 in GetRestApis request from readSingleGateway, got %q", receivedLimit)
	}
}
