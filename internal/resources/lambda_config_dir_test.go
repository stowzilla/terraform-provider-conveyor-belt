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

func TestConvertS3Buckets(t *testing.T) {
	envRefs := map[string]string{
		"images_bucket_arn": "arn:aws:s3:::custom-images-bucket",
	}

	t.Run("shorthand — bucket name with permissions", func(t *testing.T) {
		bucketsMap := map[string]interface{}{
			"images":          []interface{}{"PutObject", "GetObject"},
			"legal_documents": []interface{}{"GetObject", "GetObjectVersion"},
		}

		result := convertS3Buckets(bucketsMap, "myapp", "dev", envRefs)

		if len(result) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(result))
		}

		// Images bucket (sorted first)
		entry0 := result[0].(map[string]interface{})
		if entry0["bucket_arn"] != "arn:aws:s3:::myapp-dev-images" {
			t.Errorf("expected convention ARN, got %v", entry0["bucket_arn"])
		}
		perms := entry0["permissions"].([]interface{})
		if perms[0] != "s3:GetObject" || perms[1] != "s3:PutObject" {
			t.Errorf("expected sorted s3-prefixed permissions, got %v", perms)
		}

		// Legal documents (underscore → hyphen)
		entry1 := result[1].(map[string]interface{})
		if entry1["bucket_arn"] != "arn:aws:s3:::myapp-dev-legal-documents" {
			t.Errorf("expected hyphenated ARN, got %v", entry1["bucket_arn"])
		}
	})

	t.Run("expanded — with ref() bucket_arn", func(t *testing.T) {
		bucketsMap := map[string]interface{}{
			"custom": map[string]interface{}{
				"bucket_arn":  "ref(images_bucket_arn)",
				"permissions": []interface{}{"GetObject"},
			},
		}

		result := convertS3Buckets(bucketsMap, "myapp", "dev", envRefs)

		if len(result) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(result))
		}

		entry := result[0].(map[string]interface{})
		if entry["bucket_arn"] != "arn:aws:s3:::custom-images-bucket" {
			t.Errorf("expected resolved ref ARN, got %v", entry["bucket_arn"])
		}
	})
}

func TestConvertSNSTriggers(t *testing.T) {
	envRefs := map[string]string{
		"bounces_topic_arn": "arn:aws:sns:us-east-1:123:bounces",
	}

	t.Run("resolves ref() in topic_arn", func(t *testing.T) {
		triggersRaw := []interface{}{
			map[string]interface{}{
				"topic_arn":    "ref(bounces_topic_arn)",
				"statement_id": "AllowBounces",
			},
		}

		result := convertSNSTriggers(triggersRaw, envRefs)

		if len(result) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(result))
		}

		entry := result[0].(map[string]interface{})
		if entry["topic_arn"] != "arn:aws:sns:us-east-1:123:bounces" {
			t.Errorf("expected resolved ARN, got %v", entry["topic_arn"])
		}
		if entry["statement_id"] != "AllowBounces" {
			t.Errorf("expected statement_id=AllowBounces, got %v", entry["statement_id"])
		}
	})
}

func TestConvertSQSTriggers(t *testing.T) {
	envRefs := map[string]string{
		"notifications_queue_arn": "arn:aws:sqs:us-east-1:123:notifications",
	}

	t.Run("resolves ref() in queue_arn with batch_size", func(t *testing.T) {
		triggersRaw := []interface{}{
			map[string]interface{}{
				"queue_arn":  "ref(notifications_queue_arn)",
				"batch_size": 10,
			},
		}

		result := convertSQSTriggers(triggersRaw, envRefs)

		if len(result) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(result))
		}

		entry := result[0].(map[string]interface{})
		if entry["queue_arn"] != "arn:aws:sqs:us-east-1:123:notifications" {
			t.Errorf("expected resolved ARN, got %v", entry["queue_arn"])
		}
		if entry["batch_size"] != 10 {
			t.Errorf("expected batch_size=10, got %v", entry["batch_size"])
		}
	})
}

func TestIncludesPartialSupport(t *testing.T) {
	t.Run("underscore-prefixed files are not treated as lambdas", func(t *testing.T) {
		dir := t.TempDir()

		// Regular lambda file
		apiYAML := `default:
  timeout: 30
  memory_size: 512
`
		if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(apiYAML), 0644); err != nil {
			t.Fatal(err)
		}

		// Partial file — should NOT produce a lambda entry
		partialYAML := `default:
  timeout: 900
  memory_size: 1024
  ephemeral_storage: 2048
`
		if err := os.WriteFile(filepath.Join(dir, "_worker_defaults.yml"), []byte(partialYAML), 0644); err != nil {
			t.Fatal(err)
		}

		config, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, exists := config["_worker_defaults"]; exists {
			t.Error("underscore-prefixed file should not produce a lambda entry")
		}
		if _, exists := config["api"]; !exists {
			t.Error("expected api lambda entry")
		}
		if len(config) != 1 {
			t.Errorf("expected exactly 1 lambda entry, got %d: %v", len(config), config)
		}
	})

	t.Run("includes merges partial config under lambda config", func(t *testing.T) {
		dir := t.TempDir()

		// Partial: worker defaults
		partialYAML := `default:
  timeout: 900
  memory_size: 1024
  ephemeral_storage: 2048
  env_vars:
    QUEUE_URL: ref(queue_url)
`
		if err := os.WriteFile(filepath.Join(dir, "_worker_defaults.yml"), []byte(partialYAML), 0644); err != nil {
			t.Fatal(err)
		}

		// Lambda that includes the partial and overrides memory_size
		bgYAML := `includes: [_worker_defaults]

default:
  memory_size: 2048
  env_vars:
    WORKER_TYPE: background
`
		if err := os.WriteFile(filepath.Join(dir, "background.yml"), []byte(bgYAML), 0644); err != nil {
			t.Fatal(err)
		}

		envRefs := map[string]string{"queue_url": "https://sqs.us-east-1.amazonaws.com/123/my-queue"}

		config, err := loadLambdaConfigFromDir(dir, "dev", envRefs, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		bg, ok := config["background"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected background config, got %T", config["background"])
		}

		// timeout inherited from partial
		if bg["timeout"] != 900 {
			t.Errorf("expected timeout=900 from partial, got %v", bg["timeout"])
		}

		// memory_size overridden by lambda file
		if bg["memory_size"] != 2048 {
			t.Errorf("expected memory_size=2048 (overridden), got %v", bg["memory_size"])
		}

		// ephemeral_storage inherited from partial
		if bg["ephemeral_storage"] != 2048 {
			t.Errorf("expected ephemeral_storage=2048 from partial, got %v", bg["ephemeral_storage"])
		}

		// env_vars should be merged (both partial's and lambda's)
		envVars, ok := bg["env_vars"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected env_vars map, got %T", bg["env_vars"])
		}
		if envVars["QUEUE_URL"] != "https://sqs.us-east-1.amazonaws.com/123/my-queue" {
			t.Errorf("expected QUEUE_URL from partial, got %v", envVars["QUEUE_URL"])
		}
		if envVars["WORKER_TYPE"] != "background" {
			t.Errorf("expected WORKER_TYPE from lambda file, got %v", envVars["WORKER_TYPE"])
		}
	})

	t.Run("includes without underscore prefix still resolves", func(t *testing.T) {
		dir := t.TempDir()

		partialYAML := `default:
  timeout: 120
`
		if err := os.WriteFile(filepath.Join(dir, "_api_defaults.yml"), []byte(partialYAML), 0644); err != nil {
			t.Fatal(err)
		}

		// Reference without underscore — should still resolve
		lambdaYAML := `includes: [api_defaults]

default:
  memory_size: 256
`
		if err := os.WriteFile(filepath.Join(dir, "webhooks.yml"), []byte(lambdaYAML), 0644); err != nil {
			t.Fatal(err)
		}

		config, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		webhooks, ok := config["webhooks"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected webhooks config, got %T", config["webhooks"])
		}

		if webhooks["timeout"] != 120 {
			t.Errorf("expected timeout=120 from partial (referenced without underscore), got %v", webhooks["timeout"])
		}
		if webhooks["memory_size"] != 256 {
			t.Errorf("expected memory_size=256, got %v", webhooks["memory_size"])
		}
	})

	t.Run("multiple includes merge in order", func(t *testing.T) {
		dir := t.TempDir()

		// First partial
		partial1 := `default:
  timeout: 300
  memory_size: 512
  env_vars:
    SHARED_A: from_first
    SHARED_B: from_first
`
		if err := os.WriteFile(filepath.Join(dir, "_base.yml"), []byte(partial1), 0644); err != nil {
			t.Fatal(err)
		}

		// Second partial (overrides first)
		partial2 := `default:
  memory_size: 1024
  env_vars:
    SHARED_B: from_second
    UNIQUE_C: from_second
`
		if err := os.WriteFile(filepath.Join(dir, "_heavy.yml"), []byte(partial2), 0644); err != nil {
			t.Fatal(err)
		}

		// Lambda includes both in order
		lambdaYAML := `includes: [_base, _heavy]

default:
  env_vars:
    LAMBDA_SPECIFIC: yes
`
		if err := os.WriteFile(filepath.Join(dir, "processor.yml"), []byte(lambdaYAML), 0644); err != nil {
			t.Fatal(err)
		}

		config, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		proc, ok := config["processor"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected processor config, got %T", config["processor"])
		}

		// timeout from first partial (second doesn't override it)
		if proc["timeout"] != 300 {
			t.Errorf("expected timeout=300 from _base, got %v", proc["timeout"])
		}

		// memory_size from second partial (overrides first)
		if proc["memory_size"] != 1024 {
			t.Errorf("expected memory_size=1024 from _heavy, got %v", proc["memory_size"])
		}

		envVars, ok := proc["env_vars"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected env_vars map, got %T", proc["env_vars"])
		}

		// SHARED_A from first (not overridden)
		if envVars["SHARED_A"] != "from_first" {
			t.Errorf("expected SHARED_A=from_first, got %v", envVars["SHARED_A"])
		}
		// SHARED_B overridden by second partial
		if envVars["SHARED_B"] != "from_second" {
			t.Errorf("expected SHARED_B=from_second, got %v", envVars["SHARED_B"])
		}
		// UNIQUE_C from second partial
		if envVars["UNIQUE_C"] != "from_second" {
			t.Errorf("expected UNIQUE_C=from_second, got %v", envVars["UNIQUE_C"])
		}
		// LAMBDA_SPECIFIC from the lambda file (overrides all)
		if envVars["LAMBDA_SPECIFIC"] != "yes" {
			t.Errorf("expected LAMBDA_SPECIFIC=yes, got %v", envVars["LAMBDA_SPECIFIC"])
		}
	})

	t.Run("includes respects environment overrides in partials", func(t *testing.T) {
		dir := t.TempDir()

		partialYAML := `default: &default
  timeout: 300
  memory_size: 512

dev:
  <<: *default
  memory_size: 256

prod:
  <<: *default
  memory_size: 2048
`
		if err := os.WriteFile(filepath.Join(dir, "_env_aware.yml"), []byte(partialYAML), 0644); err != nil {
			t.Fatal(err)
		}

		lambdaYAML := `includes: [_env_aware]

default:
  timeout: 60
`
		if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(lambdaYAML), 0644); err != nil {
			t.Fatal(err)
		}

		// Test dev environment
		configDev, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		api := configDev["api"].(map[string]interface{})
		// timeout overridden by lambda file
		if api["timeout"] != 60 {
			t.Errorf("expected timeout=60 (lambda override), got %v", api["timeout"])
		}
		// memory_size from partial's dev environment
		if api["memory_size"] != 256 {
			t.Errorf("expected memory_size=256 from partial dev, got %v", api["memory_size"])
		}

		// Test prod environment
		configProd, err := loadLambdaConfigFromDir(dir, "prod", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		apiProd := configProd["api"].(map[string]interface{})
		if apiProd["memory_size"] != 2048 {
			t.Errorf("expected memory_size=2048 from partial prod, got %v", apiProd["memory_size"])
		}
	})

	t.Run("missing partial in includes is silently ignored", func(t *testing.T) {
		dir := t.TempDir()

		lambdaYAML := `includes: [_nonexistent]

default:
  timeout: 30
`
		if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(lambdaYAML), 0644); err != nil {
			t.Fatal(err)
		}

		config, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		api := config["api"].(map[string]interface{})
		if api["timeout"] != 30 {
			t.Errorf("expected timeout=30, got %v", api["timeout"])
		}
	})

	t.Run("shared.yml still works alongside partials", func(t *testing.T) {
		dir := t.TempDir()

		// shared.yml — global defaults for all lambdas (handled downstream)
		sharedYAML := `default:
  timeout: 30
  runtime: ruby3.4
`
		if err := os.WriteFile(filepath.Join(dir, "shared.yml"), []byte(sharedYAML), 0644); err != nil {
			t.Fatal(err)
		}

		// Partial — worker subset defaults
		partialYAML := `default:
  timeout: 900
  ephemeral_storage: 2048
`
		if err := os.WriteFile(filepath.Join(dir, "_worker.yml"), []byte(partialYAML), 0644); err != nil {
			t.Fatal(err)
		}

		// Lambda using the partial
		lambdaYAML := `includes: [_worker]

default:
  env_vars:
    JOB_TYPE: batch
`
		if err := os.WriteFile(filepath.Join(dir, "batch.yml"), []byte(lambdaYAML), 0644); err != nil {
			t.Fatal(err)
		}

		config, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// shared.yml still produces a "shared" entry (processed downstream)
		if _, exists := config["shared"]; !exists {
			t.Error("expected shared entry for downstream processing")
		}

		// _worker.yml does NOT produce an entry
		if _, exists := config["_worker"]; exists {
			t.Error("partial should not produce a lambda entry")
		}

		// batch.yml inherits from _worker partial
		batch := config["batch"].(map[string]interface{})
		if batch["timeout"] != 900 {
			t.Errorf("expected timeout=900 from _worker partial, got %v", batch["timeout"])
		}
		if batch["ephemeral_storage"] != 2048 {
			t.Errorf("expected ephemeral_storage=2048 from _worker partial, got %v", batch["ephemeral_storage"])
		}
	})
}

func TestRuntimePassthrough(t *testing.T) {
	dir := t.TempDir()

	yaml := `default:
  timeout: 30
  memory_size: 1024
  runtime: ruby3.4
  ephemeral_storage: 2048
`
	if err := os.WriteFile(filepath.Join(dir, "search.yml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	config, err := loadLambdaConfigFromDir(dir, "dev", nil, "myapp", "us-east-1", "123456789012")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	search := config["search"].(map[string]interface{})
	if search["runtime"] != "ruby3.4" {
		t.Errorf("expected runtime=ruby3.4, got %v", search["runtime"])
	}
	if search["ephemeral_storage"] != 2048 {
		t.Errorf("expected ephemeral_storage=2048, got %v", search["ephemeral_storage"])
	}
}

func TestLoadLambdaConfigFromDir_IamPolicyArns(t *testing.T) {
	dir := t.TempDir()

	apiYAML := `default: &default
  timeout: 30
  iam_policy_arns:
    - ref(bedrock_access_policy_arn)
    - arn:aws:iam::123456789012:policy/StaticPolicy

dev:
  <<: *default
`

	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(apiYAML), 0644); err != nil {
		t.Fatal(err)
	}

	envRefs := map[string]string{
		"bedrock_access_policy_arn": "arn:aws:iam::123456789012:policy/BedrockAccess",
	}

	result, err := loadLambdaConfigFromDir(dir, "dev", envRefs, "space-chat", "us-east-1", "123456789012")
	if err != nil {
		t.Fatal(err)
	}

	apiConfig, ok := result["api"].(map[string]interface{})
	if !ok {
		t.Fatal("expected api config to be a map")
	}

	arns, ok := apiConfig["iam_policy_arns"].([]interface{})
	if !ok {
		t.Fatalf("expected iam_policy_arns to be a list, got %T", apiConfig["iam_policy_arns"])
	}

	if len(arns) != 2 {
		t.Fatalf("expected 2 arns, got %d", len(arns))
	}

	if arns[0] != "arn:aws:iam::123456789012:policy/BedrockAccess" {
		t.Errorf("expected resolved ref, got %v", arns[0])
	}

	if arns[1] != "arn:aws:iam::123456789012:policy/StaticPolicy" {
		t.Errorf("expected static ARN, got %v", arns[1])
	}
}

func TestConvertDynamoDBTables_DasherizesTableNameInArn(t *testing.T) {
	tables := map[string]interface{}{
		"turn_events": []interface{}{"read", "write"},
	}
	out := convertDynamoDBTables(tables, "fantasy-draft", "production", "us-east-1", "111122223333")
	if len(out) != 1 {
		t.Fatalf("expected 1 table entry, got %d", len(out))
	}
	entry := out[0].(map[string]interface{})
	arn := entry["table_arn"].(string)
	want := "arn:aws:dynamodb:us-east-1:111122223333:table/fantasy-draft-production-turn-events"
	if arn != want {
		t.Errorf("table ARN must dasherize underscores.\n want: %s\n got:  %s", want, arn)
	}
}
