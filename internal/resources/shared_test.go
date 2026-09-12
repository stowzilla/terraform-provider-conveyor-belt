package resources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestDefaultSharedDirsIncludesConfig guards the packaging regression where the worker Lambda
// crashed at init with "cannot load such file -- /var/task/config/environment": config/ was not
// among the directories copied into the deployment package. config/ MUST ship so an app's boot
// file (config/environment.rb) is present for non-HTTP entry points like the worker.
func TestDefaultSharedDirsIncludesConfig(t *testing.T) {
	want := map[string]bool{"config": false, "models": false, "lib": false, "helpers": false, "templates": false}
	for _, d := range defaultSharedDirs {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for dir, present := range want {
		if !present {
			t.Errorf("defaultSharedDirs is missing %q; got %v", dir, defaultSharedDirs)
		}
	}
}

// TestLambdaSourceHashCoversConfigDir asserts config/ contributes to the source hash, i.e. it is
// actually walked as a shared directory. If config/ were excluded, changing config/environment.rb
// would not change the hash and the packaging would not pick it up.
func TestLambdaSourceHashCoversConfigDir(t *testing.T) {
	dir := t.TempDir()
	// Minimal handler file so the hash has a base.
	if err := os.WriteFile(filepath.Join(dir, "worker.rb"), []byte("# handler\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "environment.rb")
	if err := os.WriteFile(configFile, []byte("# v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h1, err := calculateLambdaSourceHash(dir, "worker", defaultSharedDirs)
	if err != nil {
		t.Fatalf("hash v1: %v", err)
	}
	// Mutate config/environment.rb; the hash must change, proving config/ is included.
	if err := os.WriteFile(configFile, []byte("# v2 changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := calculateLambdaSourceHash(dir, "worker", defaultSharedDirs)
	if err != nil {
		t.Fatalf("hash v2: %v", err)
	}
	if h1 == h2 {
		t.Errorf("source hash did not change when config/environment.rb changed; config/ is not being included (h=%s)", h1)
	}
	// Sanity: strings import used.
	if !strings.Contains(strings.Join(defaultSharedDirs, ","), "config") {
		t.Errorf("defaultSharedDirs missing config: %v", defaultSharedDirs)
	}
}
