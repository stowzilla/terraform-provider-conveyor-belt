package resources

import (
	"encoding/json"
	"testing"
)

func TestRouteDataParsesResponseContext(t *testing.T) {
	jsonData := `{
		"routes": [
			{
				"name": "get_items",
				"verb": "GET",
				"path": "/items",
				"gateway": "customer",
				"lambda": "items_index",
				"auth": "cognito",
				"tables": ["items"],
				"request_model": "",
				"response_model": "item_list_response",
				"response_context": "ops"
			}
		],
		"models": []
	}`

	var routeData RouteData
	err := json.Unmarshal([]byte(jsonData), &routeData)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if len(routeData.Routes) != 1 {
		t.Fatalf("Expected 1 route, got %d", len(routeData.Routes))
	}

	route := routeData.Routes[0]
	if route.ResponseContext != "ops" {
		t.Errorf("ResponseContext = %q, want %q", route.ResponseContext, "ops")
	}
	if route.ResponseModel != "item_list_response" {
		t.Errorf("ResponseModel = %q, want %q", route.ResponseModel, "item_list_response")
	}
}

func TestRouteDataMissingResponseContextDefaultsToEmpty(t *testing.T) {
	jsonData := `{
		"routes": [
			{
				"name": "get_items",
				"verb": "GET",
				"path": "/items",
				"gateway": "customer",
				"lambda": "items_index",
				"auth": "cognito",
				"tables": [],
				"request_model": "",
				"response_model": "item_list_response"
			}
		],
		"models": []
	}`

	var routeData RouteData
	err := json.Unmarshal([]byte(jsonData), &routeData)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if len(routeData.Routes) != 1 {
		t.Fatalf("Expected 1 route, got %d", len(routeData.Routes))
	}

	route := routeData.Routes[0]
	if route.ResponseContext != "" {
		t.Errorf("ResponseContext = %q, want empty string", route.ResponseContext)
	}
}
