// internal/resources/shared.go
package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// DispatcherConfig represents the provider configuration
type DispatcherConfig struct {
	AppName                   string
	Environment               string
	AwsRegion                 string
	AwsAccountId              string
	CognitoUserPoolArns       []string
	FrontendUrls              []string
	LambdaSourceDir           string
	LambdaConfig              map[string]interface{}
	SharedIamPolicyArns       []string
	LambdaSharedDirs          []string
	LambdaGemDirs             []string
	LambdaLayerArns           []string
	ReadOnlyTables            []string
	ReadWriteTables           []string
	AlarmConfig               *AlarmConfig
	Tags                      map[string]string
	CustomDomainName          string // Custom domain name for unified API access (e.g., "api.example.com")
	FriendlyErrors            bool   // Enable friendly error messages for missing routes
	SchemaSource              string // Path to schema.tf.rb for API Gateway model definitions
	SuppressTableEnvVars      bool   // When true, do not generate *_TABLE_NAME and TABLES env vars
	// Provider-level defaults for Lambda configuration
	DefaultLambdaTimeout   int64
	DefaultLambdaMemory    int64
	DefaultTags            map[string]string
	DockerBuildConcurrency int
	// RouteProcessingConcurrency controls the number of concurrent API Gateway route operations.
	// If <= 0, defaults to DockerBuildConcurrency or CPU count.
	// Requirements: 1.3, 3.4
	RouteProcessingConcurrency int
}

// AlarmConfig represents CloudWatch Alarm configuration
type AlarmConfig struct {
	Enabled     bool
	SnsTopicArn string

	// Error alarms
	ErrorEnabled           bool
	ErrorThreshold         int64
	ErrorPeriod            int64
	ErrorEvaluationPeriods int64
	ErrorStatistic         string

	// Duration alarms
	DurationEnabled           bool
	DurationThreshold         int64
	DurationPeriod            int64
	DurationEvaluationPeriods int64
	DurationStatistic         string

	// Throttle alarms
	ThrottleEnabled           bool
	ThrottleThreshold         int64
	ThrottlePeriod            int64
	ThrottleEvaluationPeriods int64
	ThrottleStatistic         string

	// Invocation count alarms
	InvocationsEnabled           bool
	InvocationsThreshold         int64
	InvocationsPeriod            int64
	InvocationsEvaluationPeriods int64
	InvocationsStatistic         string

	// Per-lambda overrides
	LambdaOverrides map[string]*LambdaAlarmConfig
}

// LambdaAlarmConfig holds per-lambda alarm overrides
type LambdaAlarmConfig struct {
	// Can override any global setting
	ErrorThreshold         *int64
	ErrorPeriod            *int64
	ErrorEvaluationPeriods *int64
	ErrorEnabled           *bool

	DurationThreshold         *int64
	DurationPeriod            *int64
	DurationEvaluationPeriods *int64
	DurationEnabled           *bool

	ThrottleThreshold         *int64
	ThrottlePeriod            *int64
	ThrottleEvaluationPeriods *int64
	ThrottleEnabled           *bool

	InvocationsThreshold         *int64
	InvocationsPeriod            *int64
	InvocationsEvaluationPeriods *int64
	InvocationsEnabled           *bool
}

// DispatcherClient holds provider configuration (compatible with provider.DispatcherClient)
type DispatcherClient struct {
	Environment            string
	AwsRegion              string
	DefaultLambdaTimeout   int64
	DefaultLambdaMemory    int64
	DefaultTags            map[string]string
	DockerBuildConcurrency int
}

// GetCORSOriginForConfig returns the CORS origin for DispatcherConfig
func GetCORSOriginForConfig(config *DispatcherConfig) string {
	if len(config.FrontendUrls) == 0 {
		return "http://localhost:3000" // Default for development
	}
	if len(config.FrontendUrls) == 1 {
		return config.FrontendUrls[0]
	}
	// For multiple origins, use wildcard
	return "*"
}

// buildResourceTags creates a map of tags for AWS resources
func buildResourceTags(config *DispatcherConfig, resourceType, resourceName string) map[string]string {
	tags := make(map[string]string)

	// Add user-provided tags
	for k, v := range config.Tags {
		tags[k] = v
	}

	// Add default tags
	tags["ManagedBy"] = "terraform-dispatcher"
	tags["Application"] = config.AppName
	tags["Environment"] = config.Environment
	tags["ResourceType"] = resourceType
	tags["ResourceName"] = resourceName

	return tags
}

// ResourceClients holds all AWS service clients
type ResourceClients struct {
	ApiGateway     *apigateway.Client
	Lambda         *lambda.Client
	IAM            *iam.Client
	CloudWatchLogs *cloudwatchlogs.Client
	CloudWatch     *cloudwatch.Client
	SNS            *sns.Client
	SQS            *sqs.Client
}

// RouteData represents the JSON structure returned by `belt routes -f json`
type RouteData struct {
	Routes []struct {
		Name          string   `json:"name"`
		Verb          string   `json:"verb"`
		Path          string   `json:"path"`
		Controller    string   `json:"gateway"`
		Action        string   `json:"lambda"`
		Auth          string   `json:"auth"`
		Tables        []string `json:"tables"`
		RequestModel  string   `json:"request_model"`
		ResponseModel string   `json:"response_model"`
		ResponseContext string `json:"response_context"`
	} `json:"routes"`
	Models []struct {
		Name        string                          `json:"name"`
		Description string                          `json:"description"`
		Properties  map[string]utils.ModelProperty   `json:"properties"`
		Required    []string                        `json:"required"`
	} `json:"models"`
}

// initializeClients creates AWS service clients
func initializeClients(ctx context.Context, config *DispatcherConfig) (*ResourceClients, error) {
	var awsConfig aws.Config
	var err error

	awsConfig, err = createStandardAWSConfig(ctx, config.AwsRegion)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS config: %w", err)
	}
	

	return &ResourceClients{
		ApiGateway:     apigateway.NewFromConfig(awsConfig),
		Lambda:         lambda.NewFromConfig(awsConfig),
		IAM:            iam.NewFromConfig(awsConfig),
		CloudWatchLogs: cloudwatchlogs.NewFromConfig(awsConfig),
		CloudWatch:     cloudwatch.NewFromConfig(awsConfig),
		SNS:            sns.NewFromConfig(awsConfig),
		SQS:            sqs.NewFromConfig(awsConfig),
	}, nil
}

// createStandardAWSConfig creates standard AWS config
func createStandardAWSConfig(ctx context.Context, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithRetryer(func() aws.Retryer {
			return retry.AddWithMaxAttempts(retry.NewStandard(), 3)
		}),
	)
}

// getAwsAccountId retrieves the AWS account ID using STS GetCallerIdentity
func getAwsAccountId(ctx context.Context, region string) (string, error) {
	var awsConfig aws.Config
	var err error

	// Use standard AWS configuration
	awsConfig, err = createStandardAWSConfig(ctx, region)
	if err != nil {
		return "", fmt.Errorf("failed to create AWS config: %w", err)
	}

	// Create STS client and get caller identity
	stsClient := sts.NewFromConfig(awsConfig)
	result, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("failed to get AWS account ID: %w", err)
	}

	if result.Account == nil {
		return "", fmt.Errorf("AWS account ID is nil in STS response")
	}

	return *result.Account, nil
}

// executeBeltRoutes executes `belt routes -f json` and returns parsed routes.
func executeBeltRoutes(ctx context.Context, source string) ([]utils.Route, error) {
	routes, _, err := executeBeltRoutesWithSchema(ctx, source, "")
	return routes, err
}

// executeBeltRoutesWithSchema executes `belt routes -f json` with an explicit --schema path.
func executeBeltRoutesWithSchema(ctx context.Context, source, schemaPath string) ([]utils.Route, []utils.ModelDefinition, error) {
	// Verify belt is installed
	if _, err := exec.LookPath("belt"); err != nil {
		return nil, nil, fmt.Errorf("belt CLI not found in PATH. Install with: gem install belt")
	}

	utils.Info(ctx, "Executing belt routes", map[string]interface{}{
		"source":      source,
		"schema_path": schemaPath,
	})

	// Resolve absolute path for the source file
	absSource := source
	if !filepath.IsAbs(source) {
		wd, err := os.Getwd()
		if err == nil {
			absSource = filepath.Join(wd, source)
		}
	}

	// Derive project root from the routes file path.
	// Convention: routes file is at <project_root>/infrastructure/routes.tf.rb
	projectRoot := filepath.Dir(filepath.Dir(absSource))

	args := []string{"routes", "-f", "json"}
	if schemaPath != "" {
		absSchema := schemaPath
		if !filepath.IsAbs(schemaPath) {
			wd, err := os.Getwd()
			if err == nil {
				absSchema = filepath.Join(wd, schemaPath)
			}
		}
		args = append(args, "--schema", absSchema)
	}

	cmd := exec.Command("belt", args...)
	cmd.Dir = projectRoot
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			utils.Error(ctx, "belt routes failed", map[string]interface{}{
				"stderr":      string(exitError.Stderr),
				"source":      absSource,
			})
		}
		return nil, nil, fmt.Errorf("failed to execute belt routes with source %s: %w\nstderr: %s", absSource, err, stderr.String())
	}

	// Log any stderr warnings
	if stderr.Len() > 0 {
		utils.Warn(ctx, "belt routes produced warnings", map[string]interface{}{
			"stderr": stderr.String(),
			"source": absSource,
		})
	}

	var routeData RouteData
	err = json.Unmarshal(output, &routeData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse JSON output: %w", err)
	}

	// Convert to utils.Route format
	routes := make([]utils.Route, len(routeData.Routes))
	for i, route := range routeData.Routes {
		routes[i] = utils.Route{
			Name:            route.Name,
			Verb:            route.Verb,
			Path:            route.Path,
			Gateway:         route.Controller,
			Lambda:          route.Action,
			Auth:            route.Auth,
			Tables:          route.Tables,
			RequestModel:    route.RequestModel,
			ResponseModel:   route.ResponseModel,
			ResponseContext: route.ResponseContext,
		}
	}

	// Convert to utils.ModelDefinition format
	var models []utils.ModelDefinition
	for _, m := range routeData.Models {
		models = append(models, utils.ModelDefinition{
			Name:        m.Name,
			Description: m.Description,
			Properties:  m.Properties,
			Required:    m.Required,
		})
	}

	return routes, models, nil
}

// discoverAllLambdas scans the lambda source directory for all .rb files
// and returns their lambda names (filename without .rb extension).
// These include both route-based lambdas and standalone lambdas (EventBridge, SNS, SQS triggers).
func discoverAllLambdas(ctx context.Context, lambdaSourceDir string) ([]string, error) {
	// Scan lambda source directory for .rb files (top-level only, no subdirectories)
	entries, err := os.ReadDir(lambdaSourceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read lambda source directory: %w", err)
	}

	allLambdas := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Check if it's a .rb file
		if !strings.HasSuffix(name, ".rb") {
			continue
		}

		// Extract lambda name (filename without .rb extension)
		lambda := strings.TrimSuffix(name, ".rb")
		allLambdas = append(allLambdas, lambda)
	}

	// Sort for deterministic ordering
	sort.Strings(allLambdas)

	if len(allLambdas) > 0 {
		utils.Info(ctx, "Discovered Lambda functions from filesystem", map[string]interface{}{
			"count":   len(allLambdas),
			"lambdas": allLambdas,
		})
	}

	return allLambdas, nil
}

// calculateConfigHash calculates SHA256 hash for top-level change detection.
// NOTE: This is NOT used for actual update decisions - the granular hashes
// (gateway_hashes, lambda_hashes, lambda_source_hashes, lambda_config_hashes)
// determine what actually gets updated. This hash is just for overall change detection.
//
// IMPORTANT: We deliberately exclude lambda_config (envVars parameter) because:
// 1. It contains Terraform framework types that don't marshal deterministically
// 2. Lambda config changes are already detected by lambda_config_hashes
// 3. Including it causes false hash mismatches between plan and apply
func calculateConfigHash(routes []utils.Route, models []utils.ModelDefinition, envVars map[string]interface{}, readOnlyTables, readWriteTables []string) (string, error) {
	// CRITICAL: Sort routes for deterministic hashing
	// This prevents hash changes when routes are returned in different order
	sortedRoutes := make([]utils.Route, len(routes))
	copy(sortedRoutes, routes)
	utils.SortRoutes(sortedRoutes)

	// Sort table slices for deterministic hashing
	sortedReadOnly := make([]string, len(readOnlyTables))
	copy(sortedReadOnly, readOnlyTables)
	sort.Strings(sortedReadOnly)

	sortedReadWrite := make([]string, len(readWriteTables))
	copy(sortedReadWrite, readWriteTables)
	sort.Strings(sortedReadWrite)

	// Sort models for deterministic hashing
	sortedModels := make([]utils.ModelDefinition, len(models))
	copy(sortedModels, models)
	sort.Slice(sortedModels, func(i, j int) bool {
		return sortedModels[i].Name < sortedModels[j].Name
	})

	// Create a combined structure for hashing
	// NOTE: Deliberately NOT including envVars (lambda_config) - it's covered by lambda_config_hashes
	combined := struct {
		Routes          []utils.Route
		Models          []utils.ModelDefinition
		ReadOnlyTables  []string
		ReadWriteTables []string
	}{
		Routes:          sortedRoutes,
		Models:          sortedModels,
		ReadOnlyTables:  sortedReadOnly,
		ReadWriteTables: sortedReadWrite,
	}

	combinedJSON, err := json.Marshal(combined)
	if err != nil {
		return "", fmt.Errorf("failed to marshal combined config for hashing: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(combinedJSON)
	hash := hex.EncodeToString(hasher.Sum(nil))

	return hash, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// calculateGatewayHash calculates hash for a specific gateway's routes and config
func calculateGatewayHash(routes []utils.Route, configFingerprint string) (string, error) {
	// CRITICAL: Sort routes for deterministic hashing
	sortedRoutes := make([]utils.Route, len(routes))
	copy(sortedRoutes, routes)
	utils.SortRoutes(sortedRoutes)

	routesJSON, err := json.Marshal(sortedRoutes)
	if err != nil {
		return "", fmt.Errorf("failed to marshal routes for hashing: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(routesJSON)
	if configFingerprint != "" {
		hasher.Write([]byte(configFingerprint))
	}
	hash := hex.EncodeToString(hasher.Sum(nil))

	return hash, nil
}

// hashDirectoryContents recursively hashes all files in a directory
func hashDirectoryContents(dirPath string) (string, error) {
	hasher := sha256.New()

	// Walk directory and hash all files in sorted order for determinism
	// Skip non-source files that can change without meaningful code changes
	var files []string
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			name := info.Name()
			// Skip OS metadata, editor temp files, and other non-source artifacts
			if name == ".DS_Store" ||
				strings.HasPrefix(name, ".") ||
				strings.HasSuffix(name, ".swp") ||
				strings.HasSuffix(name, ".swo") ||
				strings.HasSuffix(name, "~") {
				return nil
			}
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Sort files for deterministic hashing
	sort.Strings(files)

	// Hash each file's contents
	for _, filePath := range files {
		// Get relative path for consistent hashing
		relPath, err := filepath.Rel(dirPath, filePath)
		if err != nil {
			return "", err
		}

		// Hash the relative path first
		hasher.Write([]byte(relPath))
		hasher.Write([]byte{0}) // separator

		// Hash file contents
		content, err := os.ReadFile(filePath)
		if err != nil {
			return "", err
		}
		hasher.Write(content)
		hasher.Write([]byte{0}) // separator
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// calculateLambdaSourceHash calculates hash of Lambda source code files
func calculateLambdaSourceHash(lambdaSourceDir, lambda string, sharedDirs []string, gemDirs ...string) (string, error) {
	hasher := sha256.New()

	// Clean and convert to absolute path
	// filepath.Clean normalizes paths like ../modules/sobvibes/../../lambda to ../lambda
	cleanedPath := filepath.Clean(lambdaSourceDir)
	absLambdaSourceDir := cleanedPath
	if !filepath.IsAbs(cleanedPath) {
		var err error
		absLambdaSourceDir, err = filepath.Abs(cleanedPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve absolute path for %s: %w", lambdaSourceDir, err)
		}
	}

	// Hash main lambda file
	rubyFilePath := filepath.Join(absLambdaSourceDir, fmt.Sprintf("%s.rb", lambda))
	if content, err := os.ReadFile(rubyFilePath); err == nil {
		hasher.Write([]byte("main:"))
		hasher.Write(content)
		hasher.Write([]byte{0})
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to read %s: %w", rubyFilePath, err)
	}
	// If file doesn't exist, that's OK - we'll use placeholder

	// Hash shared directories using configured list (or default)
	if len(sharedDirs) == 0 {
		sharedDirs = []string{"models", "lib", "helpers", "templates"}
	}
	for _, dirName := range sharedDirs {
		dirPath := filepath.Join(absLambdaSourceDir, dirName)
		if info, err := os.Stat(dirPath); err == nil && info.IsDir() {
			dirHash, err := hashDirectoryContents(dirPath)
			if err != nil {
				return "", fmt.Errorf("failed to hash directory %s: %w", dirName, err)
			}
			hasher.Write([]byte(fmt.Sprintf("%s:", dirName)))
			hasher.Write([]byte(dirHash))
			hasher.Write([]byte{0})
		}
		// If directory doesn't exist, skip it
	}

	// Hash Gemfile if it exists
	gemfilePath := filepath.Join(absLambdaSourceDir, "Gemfile")
	if content, err := os.ReadFile(gemfilePath); err == nil {
		hasher.Write([]byte("gemfile:"))
		hasher.Write(content)
		hasher.Write([]byte{0})
	}

	// Hash gem directories (path-based gems in Docker build context)
	// Changes to local gem source should trigger a rebuild
	gemfileLockPath := filepath.Join(absLambdaSourceDir, "Gemfile.lock")
	if content, err := os.ReadFile(gemfileLockPath); err == nil {
		hasher.Write([]byte("gemfile_lock:"))
		hasher.Write(content)
		hasher.Write([]byte{0})
	}

	// Hash vendor/cache if present (auto-detected pre-built gems)
	vendorCachePath := filepath.Join(absLambdaSourceDir, "vendor", "cache")
	if info, err := os.Stat(vendorCachePath); err == nil && info.IsDir() {
		dirHash, err := hashDirectoryContents(vendorCachePath)
		if err == nil {
			hasher.Write([]byte("vendor_cache:"))
			hasher.Write([]byte(dirHash))
			hasher.Write([]byte{0})
		}
	}

	// Hash gem directories (path-based gems) so source changes trigger rebuilds
	for _, gemDir := range gemDirs {
		dirPath := filepath.Join(absLambdaSourceDir, gemDir)
		if info, err := os.Stat(dirPath); err == nil && info.IsDir() {
			dirHash, err := hashDirectoryContents(dirPath)
			if err == nil {
				hasher.Write([]byte(fmt.Sprintf("gem_dir_%s:", gemDir)))
				hasher.Write([]byte(dirHash))
				hasher.Write([]byte{0})
			}
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// lambdaConfigFields holds the extracted configuration fields for a lambda,
// used by both calculateLambdaHash and calculateLambdaConfigHash.
type lambdaConfigFields struct {
	Tables              []string
	EnvVars             map[string]interface{}
	DynamoDBTables      []interface{}
	S3Buckets           []interface{}
	SESEmails           []interface{}
	SNSTriggers         []interface{}
	SQSTriggers         []interface{}
	Timeout             int64
	Memory              int64
	SharedIamPolicyArns []string
}

// collectLambdaConfigFields extracts all configuration fields for a lambda from routes and lambda_config.
// This is the shared logic between calculateLambdaHash and calculateLambdaConfigHash.
func collectLambdaConfigFields(routes []utils.Route, lambdaConfig map[string]interface{}, lambdaName string, sharedIamPolicyArns []string) lambdaConfigFields {
	// Collect unique tables for this specific lambda only
	tablesSet := make(map[string]bool)
	for _, route := range routes {
		if route.Lambda == lambdaName {
			for _, table := range route.Tables {
				tablesSet[table] = true
			}
		}
	}
	tables := make([]string, 0, len(tablesSet))
	for table := range tablesSet {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	// Get lambda-specific env vars from lambda_config (shared then lambda-specific override)
	lambdaEnvVars := make(map[string]interface{})
	if sharedConfigRaw, exists := lambdaConfig["shared"]; exists {
		if sharedConfig, ok := extractMapValue(sharedConfigRaw); ok {
			if envVarsRaw, exists := sharedConfig["env_vars"]; exists {
				if envVars, ok := extractMapValue(envVarsRaw); ok {
					sharedKeys := make([]string, 0, len(envVars))
					for key := range envVars {
						sharedKeys = append(sharedKeys, key)
					}
					sort.Strings(sharedKeys)
					for _, key := range sharedKeys {
						lambdaEnvVars[key] = envVars[key]
					}
				}
			}
		}
	}
	if lambdaConfigRaw, exists := lambdaConfig[lambdaName]; exists {
		if lambdaCfg, ok := extractMapValue(lambdaConfigRaw); ok {
			if envVarsRaw, exists := lambdaCfg["env_vars"]; exists {
				if envVars, ok := extractMapValue(envVarsRaw); ok {
					lambdaKeys := make([]string, 0, len(envVars))
					for key := range envVars {
						lambdaKeys = append(lambdaKeys, key)
					}
					sort.Strings(lambdaKeys)
					for _, key := range lambdaKeys {
						lambdaEnvVars[key] = envVars[key]
					}
				}
			}
		}
	}

	// Collect resource configurations from lambda-specific config
	var dynamoDBTables, s3Buckets, sesEmails, snsTriggers, sqsTriggers []interface{}
	if lambdaConfigRaw, exists := lambdaConfig[lambdaName]; exists {
		if lambdaCfg, ok := extractMapValue(lambdaConfigRaw); ok {
			if v, exists := lambdaCfg["dynamodb_tables"]; exists {
				dynamoDBTables = convertToStableList(v)
			}
			if v, exists := lambdaCfg["s3_buckets"]; exists {
				s3Buckets = convertToStableList(v)
			}
			if v, exists := lambdaCfg["ses_emails"]; exists {
				sesEmails = convertToStableList(v)
			}
			if v, exists := lambdaCfg["sns_triggers"]; exists {
				snsTriggers = convertToStableList(v)
			}
			if v, exists := lambdaCfg["sqs_triggers"]; exists {
				sqsTriggers = convertToStableList(v)
			}
		}
	}

	// Extract timeout and memory (shared then lambda-specific override)
	var timeout, memory int64
	if sharedConfigRaw, exists := lambdaConfig["shared"]; exists {
		if sharedConfig, ok := extractMapValue(sharedConfigRaw); ok {
			if t, exists := sharedConfig["timeout"]; exists {
				if tVal, ok := extractInt64Value(t); ok {
					timeout = tVal
				}
			}
			if m, exists := sharedConfig["memory_size"]; exists {
				if mVal, ok := extractInt64Value(m); ok {
					memory = mVal
				}
			}
		}
	}
	if lambdaConfigRaw, exists := lambdaConfig[lambdaName]; exists {
		if lambdaCfg, ok := extractMapValue(lambdaConfigRaw); ok {
			if t, exists := lambdaCfg["timeout"]; exists {
				if tVal, ok := extractInt64Value(t); ok {
					timeout = tVal
				}
			}
			if m, exists := lambdaCfg["memory_size"]; exists {
				if mVal, ok := extractInt64Value(m); ok {
					memory = mVal
				}
			}
		}
	}

	// Sort shared IAM policy ARNs for deterministic hashing
	sortedSharedIamPolicyArns := make([]string, len(sharedIamPolicyArns))
	copy(sortedSharedIamPolicyArns, sharedIamPolicyArns)
	sort.Strings(sortedSharedIamPolicyArns)

	return lambdaConfigFields{
		Tables:              tables,
		EnvVars:             lambdaEnvVars,
		DynamoDBTables:      dynamoDBTables,
		S3Buckets:           s3Buckets,
		SESEmails:           sesEmails,
		SNSTriggers:         snsTriggers,
		SQSTriggers:         sqsTriggers,
		Timeout:             timeout,
		Memory:              memory,
		SharedIamPolicyArns: sortedSharedIamPolicyArns,
	}
}

// calculateLambdaHash calculates hash for a specific lambda including tables, env vars, permissions, layers, alarm config, and source code
// NOTE: Deliberately excludes route paths/verbs since those don't affect Lambda behavior
func calculateLambdaHash(routes []utils.Route, lambdaConfig map[string]interface{}, lambdaName, lambdaSourceDir string, sharedDirs []string, lambdaLayerArns []string, alarmConfig *AlarmConfig, readOnlyTables, readWriteTables []string, sharedIamPolicyArns []string) (string, error) {
	fields := collectLambdaConfigFields(routes, lambdaConfig, lambdaName, sharedIamPolicyArns)

	sourceHash, err := calculateLambdaSourceHash(lambdaSourceDir, lambdaName, sharedDirs)
	if err != nil {
		return "", fmt.Errorf("failed to hash Lambda source code for lambda %s: %w", lambdaName, err)
	}

	// Normalize nil slices to empty slices for deterministic JSON serialization.
	// json.Marshal produces "null" for nil slices but "[]" for empty slices,
	// which would cause hash differences between runs.
	combined := struct {
		Tables              []string
		EnvVars             map[string]string
		DynamoDBTables      []interface{}
		S3Buckets           []interface{}
		SESEmails           []interface{}
		LayerArns           []string
		SourceHash          string
		AlarmConfig         interface{}
		ReadOnlyTables      []string
		ReadWriteTables     []string
		Timeout             int64
		Memory              int64
		SharedIamPolicyArns []string
	}{
		Tables:              normalizeStringSlice(fields.Tables),
		EnvVars:             stabilizeEnvVarsForHashing(fields.EnvVars),
		DynamoDBTables:      normalizeInterfaceSlice(fields.DynamoDBTables),
		S3Buckets:           normalizeInterfaceSlice(fields.S3Buckets),
		SESEmails:           normalizeInterfaceSlice(fields.SESEmails),
		LayerArns:           normalizeStringSlice(lambdaLayerArns),
		SourceHash:          sourceHash,
		AlarmConfig:         alarmConfig,
		ReadOnlyTables:      normalizeStringSlice(readOnlyTables),
		ReadWriteTables:     normalizeStringSlice(readWriteTables),
		Timeout:             fields.Timeout,
		Memory:              fields.Memory,
		SharedIamPolicyArns: fields.SharedIamPolicyArns,
	}

	combinedJSON, err := json.Marshal(combined)
	if err != nil {
		return "", fmt.Errorf("failed to marshal lambda config for hashing: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(combinedJSON)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// calculateLambdaConfigHash calculates hash for Lambda configuration (env vars, permissions, layers, alarm config)
// This excludes source code so we can detect config-only changes
func calculateLambdaConfigHash(routes []utils.Route, lambdaConfig map[string]interface{}, lambdaName string, lambdaLayerArns []string, alarmConfig *AlarmConfig, readOnlyTables, readWriteTables []string, sharedIamPolicyArns []string) (string, error) {
	fields := collectLambdaConfigFields(routes, lambdaConfig, lambdaName, sharedIamPolicyArns)

	// Normalize nil slices to empty slices for deterministic JSON serialization.
	configData := struct {
		Tables              []string
		EnvVars             map[string]string
		DynamoDBTables      []interface{}
		S3Buckets           []interface{}
		SESEmails           []interface{}
		SNSTriggers         []interface{}
		SQSTriggers         []interface{}
		LayerArns           []string
		AlarmConfig         interface{}
		ReadOnlyTables      []string
		ReadWriteTables     []string
		Timeout             int64
		Memory              int64
		SharedIamPolicyArns []string
	}{
		Tables:              normalizeStringSlice(fields.Tables),
		EnvVars:             stabilizeEnvVarsForHashing(fields.EnvVars),
		DynamoDBTables:      normalizeInterfaceSlice(fields.DynamoDBTables),
		S3Buckets:           normalizeInterfaceSlice(fields.S3Buckets),
		SESEmails:           normalizeInterfaceSlice(fields.SESEmails),
		SNSTriggers:         normalizeInterfaceSlice(fields.SNSTriggers),
		SQSTriggers:         normalizeInterfaceSlice(fields.SQSTriggers),
		LayerArns:           normalizeStringSlice(lambdaLayerArns),
		AlarmConfig:         alarmConfig,
		ReadOnlyTables:      normalizeStringSlice(readOnlyTables),
		ReadWriteTables:     normalizeStringSlice(readWriteTables),
		Timeout:             fields.Timeout,
		Memory:              fields.Memory,
		SharedIamPolicyArns: fields.SharedIamPolicyArns,
	}

	configJSON, err := json.Marshal(configData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config for hashing: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(configJSON)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// calculateAllGatewayHashes returns a map of gateway name -> hash
// Includes frontendUrls in the hash so CORS config changes trigger gateway updates
func calculateAllGatewayHashes(routes []utils.Route, frontendUrls []string) (map[string]string, error) {
	gatewayGroups := utils.GroupByGateway(routes)
	hashes := make(map[string]string)

	// Build a config fingerprint from CORS-affecting attributes
	var configFingerprint string
	if len(frontendUrls) > 0 {
		sorted := make([]string, len(frontendUrls))
		copy(sorted, frontendUrls)
		sort.Strings(sorted)
		fp, _ := json.Marshal(sorted)
		configFingerprint = string(fp)
	}

	for gateway, gatewayRoutes := range gatewayGroups {
		hash, err := calculateGatewayHash(gatewayRoutes, configFingerprint)
		if err != nil {
			return nil, fmt.Errorf("failed to hash gateway %s: %w", gateway, err)
		}
		hashes[gateway] = hash
	}

	return hashes, nil
}

// normalizeStringSlice ensures a nil slice becomes an empty slice so that
// json.Marshal always produces "[]" instead of "null". This prevents hash
// differences between runs where a field is unset (nil) vs explicitly empty.
func normalizeStringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// normalizeInterfaceSlice ensures a nil slice becomes an empty slice so that
// json.Marshal always produces "[]" instead of "null".
func normalizeInterfaceSlice(s []interface{}) []interface{} {
	if s == nil {
		return []interface{}{}
	}
	return s
}

// stabilizeEnvVarsForHashing converts a map of env vars (which may contain types.String
// or other Terraform framework types) into a map of plain strings suitable for deterministic
// JSON marshaling. This is critical because types.String objects serialize as complex structs
// via json.Marshal, and their internal representation can differ between plan and state contexts,
// causing hash instability and phantom changes on every terraform plan.
func stabilizeEnvVarsForHashing(envVars map[string]interface{}) map[string]string {
	result := make(map[string]string, len(envVars))
	for key, value := range envVars {
		if strVal, ok := extractStringValue(value); ok {
			result[key] = strVal
		} else {
			// Fallback: use fmt.Sprintf for unknown types
			result[key] = fmt.Sprintf("%v", value)
		}
	}
	return result
}

// convertToStableList converts Terraform list types to a stable representation for hashing
// Also handles plain Go slices for testing purposes
func convertToStableList(value interface{}) []interface{} {
	var result []interface{}

	// Unwrap types.Dynamic before processing
	if dynVal, ok := value.(types.Dynamic); ok {
		if dynVal.IsNull() || dynVal.IsUnknown() {
			return result
		}
		return convertToStableList(dynVal.UnderlyingValue())
	}

	if tupleVal, ok := value.(types.Tuple); ok {
		for _, elem := range tupleVal.Elements() {
			if objVal, ok := elem.(types.Object); ok {
				// Convert to stable map
				stableMap := make(map[string]interface{})
				for k, v := range objVal.Attributes() {
					stableMap[k] = convertToStableValue(v)
				}
				result = append(result, stableMap)
			}
		}
	} else if listVal, ok := value.(types.List); ok {
		for _, elem := range listVal.Elements() {
			if objVal, ok := elem.(types.Object); ok {
				stableMap := make(map[string]interface{})
				for k, v := range objVal.Attributes() {
					stableMap[k] = convertToStableValue(v)
				}
				result = append(result, stableMap)
			}
		}
	} else if sliceVal, ok := value.([]map[string]interface{}); ok {
		// Handle plain Go slices of maps (for testing and direct Go usage)
		for _, elem := range sliceVal {
			// Sort keys for deterministic ordering
			stableMap := make(map[string]interface{})
			keys := make([]string, 0, len(elem))
			for k := range elem {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				stableMap[k] = elem[k]
			}
			result = append(result, stableMap)
		}
	} else if sliceVal, ok := value.([]interface{}); ok {
		// Handle []interface{} slices
		for _, elem := range sliceVal {
			if mapElem, ok := elem.(map[string]interface{}); ok {
				stableMap := make(map[string]interface{})
				keys := make([]string, 0, len(mapElem))
				for k := range mapElem {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					stableMap[k] = mapElem[k]
				}
				result = append(result, stableMap)
			}
		}
	}

	return result
}

// convertToStableValue converts Terraform types to stable Go values for hashing
func convertToStableValue(value interface{}) interface{} {
	// Unwrap types.Dynamic before processing
	if dynVal, ok := value.(types.Dynamic); ok {
		if dynVal.IsNull() || dynVal.IsUnknown() {
			return "<unknown>"
		}
		return convertToStableValue(dynVal.UnderlyingValue())
	}
	if strVal, ok := value.(types.String); ok {
		// Handle unknown values - return consistent placeholder to prevent hash changes
		if strVal.IsUnknown() {
			return "<unknown>"
		}
		return strVal.ValueString()
	} else if numVal, ok := value.(types.Number); ok {
		if numVal.IsUnknown() {
			return "<unknown>"
		}
		return numVal.String()
	} else if intVal, ok := value.(types.Int64); ok {
		if intVal.IsUnknown() {
			return "<unknown>"
		}
		if intVal.IsNull() {
			return nil
		}
		return intVal.ValueInt64()
	} else if boolVal, ok := value.(types.Bool); ok {
		if boolVal.IsUnknown() {
			return "<unknown>"
		}
		return boolVal.ValueBool()
	} else if tupleVal, ok := value.(types.Tuple); ok {
		var list []interface{}
		for _, elem := range tupleVal.Elements() {
			list = append(list, convertToStableValue(elem))
		}
		return list
	} else if listVal, ok := value.(types.List); ok {
		var list []interface{}
		for _, elem := range listVal.Elements() {
			list = append(list, convertToStableValue(elem))
		}
		return list
	}
	return value
}

// parseTerraformObjectList parses a Terraform dynamic attribute value (types.Tuple, types.List,
// []interface{}, or []map[string]interface{}) into a slice of map configs.
// This is the shared logic for parsing resource configs like dynamodb_tables, s3_buckets, ses_emails.
func parseTerraformObjectList(raw interface{}, lambda, configKey string) ([]map[string]interface{}, error) {
	switch v := raw.(type) {
	case types.Tuple:
		var configs []map[string]interface{}
		for _, elem := range v.Elements() {
			if objVal, ok := elem.(types.Object); ok {
				config := make(map[string]interface{})
				for k, val := range objVal.Attributes() {
					config[k] = val
				}
				configs = append(configs, config)
			}
		}
		return configs, nil
	case types.List:
		var tempSlice []interface{}
		diags := v.ElementsAs(context.Background(), &tempSlice, false)
		if diags.HasError() {
			return nil, fmt.Errorf("failed to parse %s list for lambda %s", configKey, lambda)
		}
		var configs []map[string]interface{}
		for _, item := range tempSlice {
			if objVal, ok := item.(types.Object); ok {
				config := make(map[string]interface{})
				for k, val := range objVal.Attributes() {
					config[k] = val
				}
				configs = append(configs, config)
			}
		}
		return configs, nil
	case []interface{}:
		var configs []map[string]interface{}
		for _, item := range v {
			if config, ok := item.(map[string]interface{}); ok {
				configs = append(configs, config)
			}
		}
		return configs, nil
	case []map[string]interface{}:
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected type for %s configuration for lambda %s: got %T", configKey, lambda, raw)
	}
}

// extractTerraformString extracts a string from a Terraform value (types.String or plain string).
func extractTerraformString(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case types.String:
		if !v.IsNull() && !v.IsUnknown() {
			return v.ValueString(), true
		}
		return "", false
	case string:
		return v, true
	default:
		return "", false
	}
}

// extractTerraformStringList extracts a []string from a Terraform value
// (types.Tuple, types.List, []interface{}, or []string).
func extractTerraformStringList(raw interface{}) ([]string, bool) {
	switch v := raw.(type) {
	case types.Tuple:
		var result []string
		for _, elem := range v.Elements() {
			if strVal, ok := elem.(types.String); ok {
				result = append(result, strVal.ValueString())
			}
		}
		return result, true
	case types.List:
		var result []string
		diags := v.ElementsAs(context.Background(), &result, false)
		if diags.HasError() {
			return nil, false
		}
		return result, true
	case []interface{}:
		var result []string
		for _, p := range v {
			if perm, ok := p.(string); ok {
				result = append(result, perm)
			} else if strVal, ok := p.(types.String); ok {
				result = append(result, strVal.ValueString())
			}
		}
		return result, true
	case []string:
		return v, true
	default:
		return nil, false
	}
}

// calculateAllLambdaHashes returns a map of lambda name -> hash
// Includes both route-based lambdas and standalone lambdas
func calculateAllLambdaHashes(lambdas []string, routes []utils.Route, lambdaConfig map[string]interface{}, lambdaSourceDir string, sharedDirs []string, lambdaLayerArns []string, alarmConfig *AlarmConfig, readOnlyTables, readWriteTables []string, sharedIamPolicyArns []string) (map[string]string, error) {
	// Build route groups for enrichment
	routeLambdaGroups := utils.GroupByLambda(routes)
	hashes := make(map[string]string)

	// Hash all lambdas (with or without routes)
	for _, lambdaName := range lambdas {
		lambdaRoutes := routeLambdaGroups[lambdaName] // Empty slice if no routes
		hash, err := calculateLambdaHash(lambdaRoutes, lambdaConfig, lambdaName, lambdaSourceDir, sharedDirs, lambdaLayerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
		if err != nil {
			return nil, fmt.Errorf("failed to hash lambda %s: %w", lambdaName, err)
		}
		hashes[lambdaName] = hash
	}

	return hashes, nil
}

// calculateAllLambdaSourceHashes returns a map of lambda name -> source code hash
func calculateAllLambdaSourceHashes(lambdas []string, lambdaSourceDir string, sharedDirs []string, gemDirs ...string) (map[string]string, error) {
	hashes := make(map[string]string)

	// Hash all lambdas
	for _, lambdaName := range lambdas {
		hash, err := calculateLambdaSourceHash(lambdaSourceDir, lambdaName, sharedDirs, gemDirs...)
		if err != nil {
			return nil, fmt.Errorf("failed to hash source for lambda %s: %w", lambdaName, err)
		}
		hashes[lambdaName] = hash
	}

	return hashes, nil
}

// calculateAllLambdaConfigHashes returns a map of lambda name -> config hash
func calculateAllLambdaConfigHashes(lambdas []string, routes []utils.Route, lambdaConfig map[string]interface{}, lambdaLayerArns []string, alarmConfig *AlarmConfig, readOnlyTables, readWriteTables []string, sharedIamPolicyArns []string) (map[string]string, error) {
	// Build route groups for enrichment
	routeLambdaGroups := utils.GroupByLambda(routes)
	hashes := make(map[string]string)

	// Hash all lambdas
	for _, lambdaName := range lambdas {
		lambdaRoutes := routeLambdaGroups[lambdaName] // Empty slice if no routes
		hash, err := calculateLambdaConfigHash(lambdaRoutes, lambdaConfig, lambdaName, lambdaLayerArns, alarmConfig, readOnlyTables, readWriteTables, sharedIamPolicyArns)
		if err != nil {
			return nil, fmt.Errorf("failed to hash config for lambda %s: %w", lambdaName, err)
		}
		hashes[lambdaName] = hash
	}

	return hashes, nil
}

// getStringValue returns the first non-empty string value
func getStringValue(values ...types.String) string {
	for _, v := range values {
		if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			return v.ValueString()
		}
	}
	return ""
}

// getStringWithDefault returns string value or default if empty
func getStringWithDefault(value types.String, defaultValue string) string {
	if !value.IsNull() && !value.IsUnknown() && value.ValueString() != "" {
		return value.ValueString()
	}
	return defaultValue
}

// ResourceDiff represents changes between old and new state
type ResourceDiff struct {
	// Gateways (API Gateways)
	NewGateways      []string                 // Gateways that need to be created
	DeletedGateways  []string                 // Gateways that need to be deleted
	ModifiedGateways map[string][]utils.Route // Gateways with route changes (name -> new routes)

	// Lambdas
	NewLambdas               []string                 // Lambdas that need to be created
	DeletedLambdas           []string                 // Lambdas that need to be deleted
	ModifiedLambdas          map[string][]utils.Route // Lambdas with ANY changes (name -> new routes)
	SourceChangedLambdas     map[string][]utils.Route // Lambdas with source code changes
	ConfigOnlyChangedLambdas map[string][]utils.Route // Lambdas with only config changes (no source change)
}

// calculateResourceDiff compares old and new state to determine what changed
func calculateResourceDiff(
	oldGatewayHashes map[string]string,
	newGatewayHashes map[string]string,
	oldLambdaHashes map[string]string,
	newLambdaHashes map[string]string,
	oldLambdaSourceHashes map[string]string,
	newLambdaSourceHashes map[string]string,
	oldLambdaConfigHashes map[string]string,
	newLambdaConfigHashes map[string]string,
	routes []utils.Route,
) *ResourceDiff {
	diff := &ResourceDiff{
		NewGateways:             []string{},
		DeletedGateways:         []string{},
		ModifiedGateways:        make(map[string][]utils.Route),
		NewLambdas:              []string{},
		DeletedLambdas:          []string{},
		ModifiedLambdas:         make(map[string][]utils.Route),
		SourceChangedLambdas:    make(map[string][]utils.Route),
		ConfigOnlyChangedLambdas: make(map[string][]utils.Route),
	}

	// Group routes for easy lookup
	gatewayGroups := utils.GroupByGateway(routes)
	lambdaGroups := utils.GroupByLambda(routes)

	// Find new and modified gateways
	for gateway, newHash := range newGatewayHashes {
		if oldHash, exists := oldGatewayHashes[gateway]; !exists {
			// New gateway
			diff.NewGateways = append(diff.NewGateways, gateway)
		} else if oldHash != newHash {
			// Modified gateway
			diff.ModifiedGateways[gateway] = gatewayGroups[gateway]
		}
	}

	// Find deleted gateways
	for gateway := range oldGatewayHashes {
		if _, exists := newGatewayHashes[gateway]; !exists {
			diff.DeletedGateways = append(diff.DeletedGateways, gateway)
		}
	}

	// Find new and modified lambdas
	for lambdaName, newHash := range newLambdaHashes {
		if _, exists := oldLambdaHashes[lambdaName]; !exists {
			// New lambda
			diff.NewLambdas = append(diff.NewLambdas, lambdaName)
		} else if newHash != oldLambdaHashes[lambdaName] {
			// Modified lambda - determine if source or config changed
			diff.ModifiedLambdas[lambdaName] = lambdaGroups[lambdaName]

			// Check if we have granular hashes in old state
			hasGranularOldHashes := len(oldLambdaSourceHashes) > 0 || len(oldLambdaConfigHashes) > 0

			if hasGranularOldHashes {
				// We have granular hashes - can determine what changed
				sourceChanged := false
				if oldSourceHash, hasOld := oldLambdaSourceHashes[lambdaName]; hasOld {
					if newSourceHash, hasNew := newLambdaSourceHashes[lambdaName]; hasNew {
						sourceChanged = (oldSourceHash != newSourceHash)
					}
				}

				configChanged := false
				if oldConfigHash, hasOld := oldLambdaConfigHashes[lambdaName]; hasOld {
					if newConfigHash, hasNew := newLambdaConfigHashes[lambdaName]; hasNew {
						configChanged = (oldConfigHash != newConfigHash)
					}
				}

				// Categorize the change
				if sourceChanged {
					diff.SourceChangedLambdas[lambdaName] = lambdaGroups[lambdaName]
				} else if configChanged {
					diff.ConfigOnlyChangedLambdas[lambdaName] = lambdaGroups[lambdaName]
				}
			} else {
				// First run after upgrade - no granular hashes in old state
				// Assume source changed to be safe (do full rebuild)
				diff.SourceChangedLambdas[lambdaName] = lambdaGroups[lambdaName]
			}
		}
	}

	// Find deleted lambdas
	for lambdaName := range oldLambdaHashes {
		if _, exists := newLambdaHashes[lambdaName]; !exists {
			diff.DeletedLambdas = append(diff.DeletedLambdas, lambdaName)
		}
	}

	return diff
}

// hasChanges returns true if there are any changes in the diff
func (d *ResourceDiff) hasChanges() bool {
	return len(d.NewGateways) > 0 ||
		len(d.DeletedGateways) > 0 ||
		len(d.ModifiedGateways) > 0 ||
		len(d.NewLambdas) > 0 ||
		len(d.DeletedLambdas) > 0 ||
		len(d.ModifiedLambdas) > 0
}

// Removed generateRandomSuffix and shouldUseRandomSuffix functions
// All resource names are now deterministic based on app_name, environment, and lambda/gateway

// computeSourceFileHash computes a hash of the routes source file content
// This is used to auto-generate the default trigger when no explicit triggers are provided
func computeSourceFileHash(sourcePath string) (string, error) {
	// Clean and convert to absolute path
	cleanedPath := filepath.Clean(sourcePath)
	absSourcePath := cleanedPath
	if !filepath.IsAbs(cleanedPath) {
		var err error
		absSourcePath, err = filepath.Abs(cleanedPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve absolute path for %s: %w", sourcePath, err)
		}
	}

	// Read the file content
	content, err := os.ReadFile(absSourcePath)
	if err != nil {
		return "", fmt.Errorf("failed to read source file %s: %w", absSourcePath, err)
	}

	// Compute SHA-256 hash of file content
	hasher := sha256.New()
	hasher.Write(content)
	hash := hex.EncodeToString(hasher.Sum(nil))

	return hash, nil
}

// VpcConfig holds VPC configuration for a Lambda function
type VpcConfig struct {
	SubnetIds        []string
	SecurityGroupIds []string
}

// extractVpcConfig extracts vpc_config from lambda_config for a specific lambda.
// Returns nil if no VPC config is found.
func extractVpcConfig(lambdaConfig map[string]interface{}, lambdaName string) *VpcConfig {
	lambdaConfigRaw, exists := lambdaConfig[lambdaName]
	if !exists {
		return nil
	}

	lambdaCfg, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return nil
	}

	vpcRaw, exists := lambdaCfg["vpc_config"]
	if !exists {
		return nil
	}

	vpcMap, ok := extractMapValue(vpcRaw)
	if !ok {
		return nil
	}

	cfg := &VpcConfig{}

	if subnetsRaw, exists := vpcMap["subnet_ids"]; exists {
		cfg.SubnetIds = extractStringSlice(subnetsRaw)
	}
	if sgsRaw, exists := vpcMap["security_group_ids"]; exists {
		cfg.SecurityGroupIds = extractStringSlice(sgsRaw)
	}

	if len(cfg.SubnetIds) == 0 || len(cfg.SecurityGroupIds) == 0 {
		return nil
	}

	return cfg
}

// extractStringSlice extracts a list of strings from Terraform types (Tuple, List) or plain Go slices.
func extractStringSlice(value interface{}) []string {
	var result []string

	switch v := value.(type) {
	case types.Dynamic:
		// DynamicAttribute wraps values in types.Dynamic — unwrap and recurse
		if !v.IsNull() && !v.IsUnknown() {
			return extractStringSlice(v.UnderlyingValue())
		}
	case types.Tuple:
		for _, elem := range v.Elements() {
			if s, ok := extractStringValue(elem); ok {
				result = append(result, s)
			}
		}
	case types.List:
		for _, elem := range v.Elements() {
			if s, ok := extractStringValue(elem); ok {
				result = append(result, s)
			}
		}
	case []interface{}:
		for _, elem := range v {
			if s, ok := elem.(string); ok {
				result = append(result, s)
			} else if s, ok := extractStringValue(elem); ok {
				result = append(result, s)
			}
		}
	case []string:
		result = v
	}

	return result
}

// GetModelsForGateway filters model definitions to only those referenced by routes on the given gateway.
func GetModelsForGateway(routes []utils.Route, allModels []utils.ModelDefinition, gateway string) []utils.ModelDefinition {
	needed := make(map[string]bool)
	for _, r := range routes {
		if r.Gateway == gateway {
			if r.RequestModel != "" {
				needed[r.RequestModel] = true
			}
			if r.ResponseModel != "" {
				responseContext := r.ResponseContext
				if responseContext == "" {
					responseContext = gateway
				}
				fullName := r.ResponseModel + "_" + responseContext + "_response"
				needed[fullName] = true
			}
		}
	}

	var result []utils.ModelDefinition
	for _, m := range allModels {
		if needed[m.Name] {
			result = append(result, m)
		}
	}
	return result
}

// CalculateModelHash computes a deterministic hash of model definitions for change detection.
func CalculateModelHash(models []utils.ModelDefinition) (string, error) {
	if len(models) == 0 {
		return "", nil
	}

	sorted := make([]utils.ModelDefinition, len(models))
	copy(sorted, models)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	b, err := json.Marshal(sorted)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(b)
	return fmt.Sprintf("%x", hash[:8]), nil
}

// snakeToPascal converts snake_case to PascalCase (e.g., "create_item" → "CreateItem").
func snakeToPascal(s string) string {
	parts := strings.Split(s, "_")
	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}

// propertiesToSchema converts model properties to JSON Schema format.
func propertiesToSchema(props map[string]utils.ModelProperty) map[string]interface{} {
	result := make(map[string]interface{})
	for name, prop := range props {
		result[name] = propertyToSchema(prop)
	}
	return result
}

func propertyToSchema(prop utils.ModelProperty) map[string]interface{} {
	s := map[string]interface{}{"type": mapType(prop.Type)}

	if prop.Format != "" {
		s["format"] = prop.Format
	}
	if prop.Description != "" {
		s["description"] = prop.Description
	}
	if len(prop.Enum) > 0 {
		s["enum"] = prop.Enum
	}
	if prop.MaxLength > 0 {
		s["maxLength"] = prop.MaxLength
	}
	if prop.MinLength > 0 {
		s["minLength"] = prop.MinLength
	}
	if prop.Type == "array" && prop.Items != nil {
		s["items"] = propertyToSchema(*prop.Items)
	}
	if prop.Type == "object" && len(prop.Properties) > 0 {
		s["properties"] = propertiesToSchema(prop.Properties)
		if len(prop.Required) > 0 {
			s["required"] = prop.Required
		}
	}

	return s
}

// deployedModelFingerprint fetches models from an API Gateway and returns a canonical hash
// of what's actually deployed. This is used for drift detection in Read.
func deployedModelFingerprint(ctx context.Context, client *apigateway.Client, apiID string) (string, error) {
	input := &apigateway.GetModelsInput{
		RestApiId: aws.String(apiID),
		Limit:     aws.Int32(500),
	}
	output, err := client.GetModels(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to get models for API %s: %w", apiID, err)
	}

	if len(output.Items) == 0 {
		return "", nil
	}

	// Filter out the built-in "Empty" and "Error" models that API Gateway creates automatically
	type modelEntry struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	}
	var entries []modelEntry
	for _, m := range output.Items {
		if m.Name == nil || m.Schema == nil {
			continue
		}
		name := *m.Name
		if name == "Empty" || name == "Error" {
			continue
		}
		// Normalize schema JSON by re-marshaling through map to get consistent key ordering
		var schemaMap map[string]interface{}
		if err := json.Unmarshal([]byte(*m.Schema), &schemaMap); err != nil {
			// If schema isn't valid JSON, use raw string
			entries = append(entries, modelEntry{Name: name, Schema: *m.Schema})
			continue
		}
		// Strip fields that API Gateway adds automatically (e.g., "format": "int32"
		// on integer properties) but which the provider's generator doesn't produce.
		// Without this, the fingerprint of deployed models will never match the expected
		// fingerprint, causing perpetual drift detection.
		stripAPIGatewayAddedFields(schemaMap)
		normalized, _ := json.Marshal(schemaMap)
		entries = append(entries, modelEntry{Name: name, Schema: string(normalized)})
	}

	if len(entries) == 0 {
		return "", nil
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8]), nil
}

// stripAPIGatewayAddedFields removes fields that API Gateway adds to model schemas
// during PutRestApi but which the provider's generator doesn't produce.
// For example, API Gateway adds "format": "int32" to integer properties.
func stripAPIGatewayAddedFields(schema map[string]interface{}) {
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return
	}
	for _, propVal := range props {
		propMap, ok := propVal.(map[string]interface{})
		if !ok {
			continue
		}
		delete(propMap, "format")
	}
}

// expectedDeployedModelFingerprint computes the same canonical hash that deployedModelFingerprint
// would return if the models from the routes file were correctly deployed.
func expectedDeployedModelFingerprint(models []utils.ModelDefinition) (string, error) {
	if len(models) == 0 {
		return "", nil
	}

	type modelEntry struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	}

	gen := &OpenAPIGenerator{} // config not needed for modelToOpenAPISchema
	var entries []modelEntry
	for _, m := range models {
		pascalName := snakeToPascal(m.Name)
		schemaObj := gen.modelToOpenAPISchema(m)
		// Normalize by marshaling to JSON
		normalized, err := json.Marshal(schemaObj)
		if err != nil {
			return "", err
		}
		entries = append(entries, modelEntry{Name: pascalName, Schema: string(normalized)})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8]), nil
}

// mapType converts DSL types to JSON Schema types.
func mapType(t string) string {
	switch strings.ToLower(t) {
	case "string":
		return "string"
	case "integer", "int":
		return "integer"
	case "number", "float", "decimal":
		return "number"
	case "boolean", "bool":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "string"
	}
}
