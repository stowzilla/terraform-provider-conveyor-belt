package resources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLambdaConfigFromDir(t *testing.T) {
	// Create temp directory with YAML files
	dir := t.TempDir()

	// Write customer.yml
	customerYAML := `default: &default
  timeout: 60
  memory_size: 512
  env_vars:
    STATIC_VAR: "hello"
    COGNITO_ID: ref(cognito_user_pool_id)
    BUCKET: ref(images_bucket)
  dynamodb_tables:
    slots:
      permissions: [BatchWriteItem]
      indexes:
        SponsorIndex: [Query]
    users: [BatchGetItem]
    sponsors: [BatchGetItem, PutItem, GetItem]

dev:
  <<: *default
  memory_size: 256

prod:
  <<: *default
  memory_size: 1024
  timeout: 30
`
	if err := os.WriteFile(filepath.Join(dir, "customer.yml"), []byte(customerYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Write worker.yml
	workerYAML := `default:
  timeout: 300
  memory_size: 256
`
	if err := os.WriteFile(filepath.Join(dir, "worker.yml"), []byte(workerYAML), 0644); err != nil {
		t.Fatal(err)
	}

	envRefs := map[string]string{
		"cognito_user_pool_id": "us-east-1_ABC123",
		"images_bucket":        "my-app-dev-images",
	}

	t.Run("loads dev environment with ref resolution", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir(dir, "dev", envRefs, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if config == nil {
			t.Fatal("expected config, got nil")
		}

		customer, ok := config["customer"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected customer config to be a map, got %T", config["customer"])
		}

		if timeout, ok := customer["timeout"].(int); !ok || timeout != 60 {
			t.Errorf("expected customer timeout=60 for dev, got %v", customer["timeout"])
		}

		if memory, ok := customer["memory_size"].(int); !ok || memory != 256 {
			t.Errorf("expected customer memory_size=256 for dev, got %v", customer["memory_size"])
		}

		// Check env_vars with ref() resolution
		envVars, ok := customer["env_vars"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected env_vars to be a map, got %T", customer["env_vars"])
		}

		if envVars["STATIC_VAR"] != "hello" {
			t.Errorf("expected STATIC_VAR=hello, got %v", envVars["STATIC_VAR"])
		}
		if envVars["COGNITO_ID"] != "us-east-1_ABC123" {
			t.Errorf("expected COGNITO_ID to be resolved from ref, got %v", envVars["COGNITO_ID"])
		}
		if envVars["BUCKET"] != "my-app-dev-images" {
			t.Errorf("expected BUCKET to be resolved from ref, got %v", envVars["BUCKET"])
		}

		// Check DynamoDB tables converted to array format
		tables, ok := customer["dynamodb_tables"].([]interface{})
		if !ok {
			t.Fatalf("expected dynamodb_tables to be a slice, got %T", customer["dynamodb_tables"])
		}

		// Should have 4 entries: slots table, slots/index/SponsorIndex, sponsors table, users table
		if len(tables) != 4 {
			t.Fatalf("expected 4 dynamodb_tables entries, got %d: %v", len(tables), tables)
		}

		// Check first entry (slots table — sorted alphabetically, slots before sponsors before users)
		entry0 := tables[0].(map[string]interface{})
		expectedArn := "arn:aws:dynamodb:us-east-1:123456789012:table/myapp-dev-slots"
		if entry0["table_arn"] != expectedArn {
			t.Errorf("expected table_arn=%s, got %v", expectedArn, entry0["table_arn"])
		}
		perms0 := entry0["permissions"].([]interface{})
		if len(perms0) != 1 || perms0[0] != "dynamodb:BatchWriteItem" {
			t.Errorf("expected permissions=[dynamodb:BatchWriteItem], got %v", perms0)
		}

		// Check second entry (slots/index/SponsorIndex)
		entry1 := tables[1].(map[string]interface{})
		expectedIndexArn := expectedArn + "/index/SponsorIndex"
		if entry1["table_arn"] != expectedIndexArn {
			t.Errorf("expected table_arn=%s, got %v", expectedIndexArn, entry1["table_arn"])
		}
		perms1 := entry1["permissions"].([]interface{})
		if len(perms1) != 1 || perms1[0] != "dynamodb:Query" {
			t.Errorf("expected permissions=[dynamodb:Query], got %v", perms1)
		}

		// Check third entry (sponsors table — shorthand)
		entry2 := tables[2].(map[string]interface{})
		expectedSponsorsArn := "arn:aws:dynamodb:us-east-1:123456789012:table/myapp-dev-sponsors"
		if entry2["table_arn"] != expectedSponsorsArn {
			t.Errorf("expected table_arn=%s, got %v", expectedSponsorsArn, entry2["table_arn"])
		}
		perms2 := entry2["permissions"].([]interface{})
		if len(perms2) != 3 || perms2[0] != "dynamodb:BatchGetItem" {
			t.Errorf("expected first permission=dynamodb:BatchGetItem, got %v", perms2)
		}

		// Check fourth entry (users table — shorthand)
		entry3 := tables[3].(map[string]interface{})
		expectedUsersArn := "arn:aws:dynamodb:us-east-1:123456789012:table/myapp-dev-users"
		if entry3["table_arn"] != expectedUsersArn {
			t.Errorf("expected table_arn=%s, got %v", expectedUsersArn, entry3["table_arn"])
		}

		// Check worker config
		worker, ok := config["worker"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected worker config to be a map, got %T", config["worker"])
		}

		if timeout, ok := worker["timeout"].(int); !ok || timeout != 300 {
			t.Errorf("expected worker timeout=300, got %v", worker["timeout"])
		}
	})

	t.Run("loads prod environment with overrides", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir(dir, "prod", envRefs, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		customer, ok := config["customer"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected customer config to be a map, got %T", config["customer"])
		}

		if timeout, ok := customer["timeout"].(int); !ok || timeout != 30 {
			t.Errorf("expected customer timeout=30 for prod, got %v", customer["timeout"])
		}

		if memory, ok := customer["memory_size"].(int); !ok || memory != 1024 {
			t.Errorf("expected customer memory_size=1024 for prod, got %v", customer["memory_size"])
		}
	})

	t.Run("returns nil for empty dir path", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir("", "dev", envRefs, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if config != nil {
			t.Errorf("expected nil, got %v", config)
		}
	})

	t.Run("returns nil for nonexistent directory", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir("/nonexistent/path", "dev", envRefs, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if config != nil {
			t.Errorf("expected nil, got %v", config)
		}
	})

	t.Run("unresolved ref returns empty string", func(t *testing.T) {
		refDir := t.TempDir()
		yaml := `default:
  env_vars:
    MISSING: ref(does_not_exist)
`
		if err := os.WriteFile(filepath.Join(refDir, "api.yml"), []byte(yaml), 0644); err != nil {
			t.Fatal(err)
		}

		config, err := loadLambdaConfigFromDir(refDir, "dev", envRefs, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		api := config["api"].(map[string]interface{})
		envVars := api["env_vars"].(map[string]interface{})
		if envVars["MISSING"] != "" {
			t.Errorf("expected unresolved ref to be empty string, got %v", envVars["MISSING"])
		}
	})
}

func TestParseRefMarker(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantOk   bool
	}{
		{"ref(cognito_user_pool_id)", "cognito_user_pool_id", true},
		{"ref( spaced )", "spaced", true},
		{"ref()", "", false},
		{"not_a_ref", "", false},
		{"ref(partial", "", false},
		{"plain string value", "", false},
	}

	for _, tt := range tests {
		name, ok := parseRefMarker(tt.input)
		if ok != tt.wantOk || name != tt.wantName {
			t.Errorf("parseRefMarker(%q) = (%q, %v), want (%q, %v)", tt.input, name, ok, tt.wantName, tt.wantOk)
		}
	}
}

func TestNormalizePermissions(t *testing.T) {
	t.Run("adds dynamodb prefix", func(t *testing.T) {
		input := []interface{}{"BatchWriteItem", "Query", "GetItem"}
		result := normalizePermissions(input)
		expected := []string{"dynamodb:BatchWriteItem", "dynamodb:GetItem", "dynamodb:Query"}
		if len(result) != len(expected) {
			t.Fatalf("expected %d permissions, got %d", len(expected), len(result))
		}
		for i, p := range result {
			if p != expected[i] {
				t.Errorf("expected %s, got %s", expected[i], p)
			}
		}
	})

	t.Run("preserves existing prefix", func(t *testing.T) {
		input := []interface{}{"dynamodb:PutItem", "s3:GetObject"}
		result := normalizePermissions(input)
		if result[0] != "dynamodb:PutItem" {
			t.Errorf("expected dynamodb:PutItem, got %s", result[0])
		}
		if result[1] != "s3:GetObject" {
			t.Errorf("expected s3:GetObject, got %s", result[1])
		}
	})
}
func TestMergeLambdaConfigs(t *testing.T) {
	t.Run("TF env_vars override YAML env_vars per key", func(t *testing.T) {
		yamlConfig := map[string]interface{}{
			"customer": map[string]interface{}{
				"timeout":     60,
				"memory_size": 256,
				"env_vars": map[string]interface{}{
					"COGNITO_ID": "from-yaml",
					"STATIC_VAR": "keep-this",
				},
			},
		}

		tfConfig := map[string]interface{}{
			"customer": map[string]interface{}{
				"env_vars": map[string]interface{}{
					"COGNITO_ID": "from-terraform",
				},
			},
		}

		merged := mergeLambdaConfigs(yamlConfig, tfConfig)
		customer := merged["customer"].(map[string]interface{})
		envVars := customer["env_vars"].(map[string]interface{})

		if envVars["COGNITO_ID"] != "from-terraform" {
			t.Errorf("expected TF value to override YAML, got %v", envVars["COGNITO_ID"])
		}
		if envVars["STATIC_VAR"] != "keep-this" {
			t.Errorf("expected YAML value to be preserved, got %v", envVars["STATIC_VAR"])
		}
		if customer["timeout"] != 60 {
			t.Errorf("expected timeout=60 from YAML, got %v", customer["timeout"])
		}
	})

	t.Run("dynamodb_tables are concatenated", func(t *testing.T) {
		yamlConfig := map[string]interface{}{
			"api": map[string]interface{}{
				"dynamodb_tables": []interface{}{
					map[string]interface{}{"table_arn": "arn:yaml-table", "permissions": []interface{}{"dynamodb:GetItem"}},
				},
			},
		}

		tfConfig := map[string]interface{}{
			"api": map[string]interface{}{
				"dynamodb_tables": []interface{}{
					map[string]interface{}{"table_arn": "arn:tf-table", "permissions": []interface{}{"dynamodb:PutItem"}},
				},
			},
		}

		merged := mergeLambdaConfigs(yamlConfig, tfConfig)
		api := merged["api"].(map[string]interface{})
		tables := api["dynamodb_tables"].([]interface{})

		if len(tables) != 2 {
			t.Fatalf("expected 2 dynamodb_tables entries (concatenated), got %d", len(tables))
		}
	})

	t.Run("nil YAML returns TF config", func(t *testing.T) {
		tfConfig := map[string]interface{}{"api": map[string]interface{}{"timeout": 30}}
		result := mergeLambdaConfigs(nil, tfConfig)
		if result["api"] == nil {
			t.Error("expected TF config to be returned")
		}
	})

	t.Run("nil TF returns YAML config", func(t *testing.T) {
		yamlConfig := map[string]interface{}{"api": map[string]interface{}{"timeout": 60}}
		result := mergeLambdaConfigs(yamlConfig, nil)
		if result["api"] == nil {
			t.Error("expected YAML config to be returned")
		}
	})
}

func TestConvertDynamoDBTables_Shorthand(t *testing.T) {
	tablesMap := map[string]interface{}{
		// Shorthand: table name → list of permissions
		"users":    []interface{}{"BatchGetItem"},
		"sponsors": []interface{}{"BatchGetItem", "PutItem", "GetItem"},
		// Expanded: table name → map with indexes
		"slots": map[string]interface{}{
			"permissions": nil, // no table-level permissions
			"indexes": map[string]interface{}{
				"SponsorIndex": map[string]interface{}{
					"permissions": []interface{}{"Query"},
				},
			},
		},
	}

	result := convertDynamoDBTables(tablesMap, "myapp", "dev", "us-east-1", "123456789012")

	// slots has nil permissions, so only the index entry
	// sponsors has 3 permissions
	// users has 1 permission
	// Total: 1 (slots/index) + 1 (sponsors) + 1 (users) = 3
	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(result), result)
	}

	// Verify slots only has the index (no table-level entry due to nil permissions)
	foundSlotsTable := false
	foundSlotsIndex := false
	for _, entry := range result {
		e := entry.(map[string]interface{})
		arn := e["table_arn"].(string)
		if arn == "arn:aws:dynamodb:us-east-1:123456789012:table/myapp-dev-slots" {
			foundSlotsTable = true
		}
		if arn == "arn:aws:dynamodb:us-east-1:123456789012:table/myapp-dev-slots/index/SponsorIndex" {
			foundSlotsIndex = true
		}
	}

	if foundSlotsTable {
		t.Error("did not expect slots table entry (permissions was nil)")
	}
	if !foundSlotsIndex {
		t.Error("expected slots/index/SponsorIndex entry")
	}

	// Verify shorthand users entry
	foundUsers := false
	for _, entry := range result {
		e := entry.(map[string]interface{})
		arn := e["table_arn"].(string)
		if arn == "arn:aws:dynamodb:us-east-1:123456789012:table/myapp-dev-users" {
			foundUsers = true
			perms := e["permissions"].([]interface{})
			if len(perms) != 1 || perms[0] != "dynamodb:BatchGetItem" {
				t.Errorf("expected [dynamodb:BatchGetItem], got %v", perms)
			}
		}
	}
	if !foundUsers {
		t.Error("expected users table entry from shorthand syntax")
	}
}
