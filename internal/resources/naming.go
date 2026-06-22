// internal/resources/naming.go
package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// ResourceType represents the type of AWS resource being named
type ResourceType string

const (
	ResourceTypeLambda     ResourceType = "lambda"
	ResourceTypeGateway    ResourceType = "gateway"
	ResourceTypeIAMRole    ResourceType = "lambda-role"
	ResourceTypeLogGroup   ResourceType = "log"
	ResourceTypeAlarm      ResourceType = "alarm"
)

// AWS naming constraints
const (
	// Lambda function names: 1-64 characters, alphanumeric, hyphens, underscores
	LambdaNameMaxLength = 64
	// IAM role names: 1-64 characters, alphanumeric, plus, equals, comma, period, at, underscore, hyphen
	IAMRoleNameMaxLength = 64
	// API Gateway REST API names: 1-128 characters
	APIGatewayNameMaxLength = 128
	// CloudWatch Log Group names: 1-512 characters
	LogGroupNameMaxLength = 512
	// CloudWatch Alarm names: 1-255 characters
	AlarmNameMaxLength = 255
	// Hash suffix length (8 characters provides good uniqueness)
	HashSuffixLength = 8
)

// NamingConfig holds configuration for resource naming
type NamingConfig struct {
	AppName     string
	Environment string
}

// GenerateResourceName generates a deterministic resource name following the pattern:
// {app_name}-{environment}-{name} or {app_name}-{environment}-{type}-{name}
// No random suffixes are used - names are fully deterministic.
func GenerateResourceName(appName, environment, name string, resourceType ResourceType) string {
	var baseName string
	
	switch resourceType {
	case ResourceTypeIAMRole:
		// IAM roles include the type suffix: {app_name}-{environment}-{name}-lambda-role
		baseName = fmt.Sprintf("%s-%s-%s-lambda-role", appName, environment, name)
	case ResourceTypeLambda, ResourceTypeGateway:
		// Lambda and Gateway use simple pattern: {app_name}-{environment}-{name}
		baseName = fmt.Sprintf("%s-%s-%s", appName, environment, name)
	case ResourceTypeLogGroup:
		// Log groups use AWS path format: /aws/lambda/{app_name}-{environment}-{name}
		baseName = fmt.Sprintf("/aws/lambda/%s-%s-%s", appName, environment, name)
	case ResourceTypeAlarm:
		// Alarms include type: {app_name}-{environment}-{name}-alarm
		baseName = fmt.Sprintf("%s-%s-%s-alarm", appName, environment, name)
	default:
		baseName = fmt.Sprintf("%s-%s-%s", appName, environment, name)
	}
	
	// Get the max length for this resource type
	maxLength := getMaxLengthForResourceType(resourceType)
	
	// Truncate with hash if needed
	return truncateWithHash(baseName, maxLength)
}

// getMaxLengthForResourceType returns the maximum allowed name length for a resource type
func getMaxLengthForResourceType(resourceType ResourceType) int {
	switch resourceType {
	case ResourceTypeLambda:
		return LambdaNameMaxLength
	case ResourceTypeIAMRole:
		return IAMRoleNameMaxLength
	case ResourceTypeGateway:
		return APIGatewayNameMaxLength
	case ResourceTypeLogGroup:
		return LogGroupNameMaxLength
	case ResourceTypeAlarm:
		return AlarmNameMaxLength
	default:
		return LambdaNameMaxLength // Default to most restrictive
	}
}

// truncateWithHash truncates a name to fit within maxLength, adding a deterministic
// hash suffix if truncation is needed. The hash is based on the full intended name
// to ensure uniqueness even after truncation.
func truncateWithHash(name string, maxLength int) string {
	if len(name) <= maxLength {
		return name
	}
	
	// Calculate hash of the full name for uniqueness
	hash := calculateNameHash(name)
	
	// Reserve space for hyphen and hash suffix
	// Format: {truncated_name}-{hash}
	truncateLength := maxLength - HashSuffixLength - 1 // -1 for the hyphen
	
	if truncateLength < 1 {
		// Edge case: maxLength is too small, just use hash
		return hash[:maxLength]
	}
	
	truncatedName := name[:truncateLength]
	return fmt.Sprintf("%s-%s", truncatedName, hash[:HashSuffixLength])
}

// calculateNameHash calculates a deterministic SHA256 hash of the name
// and returns the first 8 characters of the hex-encoded hash
func calculateNameHash(name string) string {
	hasher := sha256.New()
	hasher.Write([]byte(name))
	fullHash := hex.EncodeToString(hasher.Sum(nil))
	return fullHash[:HashSuffixLength]
}


// ValidationError represents a naming validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Validation regex patterns for AWS resource names
var (
	// Lambda: alphanumeric, hyphens, underscores (no leading hyphen/underscore)
	lambdaNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	
	// IAM Role: alphanumeric, plus, equals, comma, period, at, underscore, hyphen
	iamRoleNameRegex = regexp.MustCompile(`^[a-zA-Z0-9+=,.@_-]+$`)
	
	// API Gateway: alphanumeric, spaces, hyphens, underscores, periods
	apiGatewayNameRegex = regexp.MustCompile(`^[a-zA-Z0-9 _.-]+$`)
	
	// CloudWatch Alarm: alphanumeric, hyphens, underscores, periods, colons
	alarmNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_:.-]+$`)
	
	// CloudWatch Log Group: alphanumeric, hyphens, underscores, periods, forward slashes
	logGroupNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)
)

// nameValidationRule defines the parameters for validating a resource name.
type nameValidationRule struct {
	field    string
	maxLen   int
	regex    *regexp.Regexp
	message  string
}

var nameValidationRules = map[ResourceType]nameValidationRule{
	ResourceTypeLambda:   {"lambda_name", LambdaNameMaxLength, lambdaNameRegex, "name must start with alphanumeric and contain only alphanumeric characters, hyphens, and underscores"},
	ResourceTypeIAMRole:  {"iam_role_name", IAMRoleNameMaxLength, iamRoleNameRegex, "name must contain only alphanumeric characters, plus, equals, comma, period, at, underscore, and hyphen"},
	ResourceTypeGateway:  {"api_gateway_name", APIGatewayNameMaxLength, apiGatewayNameRegex, "name must contain only alphanumeric characters, spaces, hyphens, underscores, and periods"},
	ResourceTypeAlarm:    {"alarm_name", AlarmNameMaxLength, alarmNameRegex, "name must contain only alphanumeric characters, hyphens, underscores, periods, and colons"},
	ResourceTypeLogGroup: {"log_group_name", LogGroupNameMaxLength, logGroupNameRegex, "name must contain only alphanumeric characters, hyphens, underscores, periods, and forward slashes"},
}

func validateName(name string, rule nameValidationRule) error {
	if len(name) == 0 {
		return &ValidationError{Field: rule.field, Message: "name cannot be empty"}
	}
	if len(name) > rule.maxLen {
		return &ValidationError{
			Field:   rule.field,
			Message: fmt.Sprintf("name exceeds maximum length of %d characters (got %d)", rule.maxLen, len(name)),
		}
	}
	if !rule.regex.MatchString(name) {
		return &ValidationError{Field: rule.field, Message: rule.message}
	}
	return nil
}

// ValidateLambdaName validates a Lambda function name against AWS constraints
// Lambda names must be 1-64 characters, alphanumeric with hyphens and underscores
func ValidateLambdaName(name string) error {
	return validateName(name, nameValidationRules[ResourceTypeLambda])
}

// ValidateIAMRoleName validates an IAM role name against AWS constraints
// IAM role names must be 1-64 characters with specific allowed characters
func ValidateIAMRoleName(name string) error {
	return validateName(name, nameValidationRules[ResourceTypeIAMRole])
}

// ValidateAPIGatewayName validates an API Gateway REST API name against AWS constraints
// API Gateway names must be 1-128 characters
func ValidateAPIGatewayName(name string) error {
	return validateName(name, nameValidationRules[ResourceTypeGateway])
}

// ValidateCloudWatchAlarmName validates a CloudWatch Alarm name against AWS constraints
// Alarm names must be 1-255 characters
func ValidateCloudWatchAlarmName(name string) error {
	return validateName(name, nameValidationRules[ResourceTypeAlarm])
}

// ValidateLogGroupName validates a CloudWatch Log Group name against AWS constraints
// Log group names must be 1-512 characters
func ValidateLogGroupName(name string) error {
	return validateName(name, nameValidationRules[ResourceTypeLogGroup])
}

// ValidateResourceName validates a resource name based on its type
func ValidateResourceName(name string, resourceType ResourceType) error {
	switch resourceType {
	case ResourceTypeLambda:
		return ValidateLambdaName(name)
	case ResourceTypeIAMRole:
		return ValidateIAMRoleName(name)
	case ResourceTypeGateway:
		return ValidateAPIGatewayName(name)
	case ResourceTypeAlarm:
		return ValidateCloudWatchAlarmName(name)
	case ResourceTypeLogGroup:
		return ValidateLogGroupName(name)
	default:
		return ValidateLambdaName(name) // Default to most restrictive
	}
}

// SanitizeName removes or replaces invalid characters from a name component
// This is useful for sanitizing user input before generating resource names
func SanitizeName(name string) string {
	// Replace spaces with hyphens
	sanitized := strings.ReplaceAll(name, " ", "-")
	
	// Replace underscores with hyphens for consistency
	sanitized = strings.ReplaceAll(sanitized, "_", "-")
	
	// Remove any characters that aren't alphanumeric or hyphens
	var result strings.Builder
	for _, r := range sanitized {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	
	sanitized = result.String()
	
	// Remove leading/trailing hyphens
	sanitized = strings.Trim(sanitized, "-")
	
	// Collapse multiple consecutive hyphens
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}
	
	// Convert to lowercase for consistency
	return strings.ToLower(sanitized)
}

// GenerateValidResourceName generates a resource name that is guaranteed to be valid
// for the specified resource type. It sanitizes inputs and handles truncation.
func GenerateValidResourceName(appName, environment, name string, resourceType ResourceType) (string, error) {
	// Sanitize input components
	sanitizedAppName := SanitizeName(appName)
	sanitizedEnv := SanitizeName(environment)
	sanitizedName := SanitizeName(name)
	
	if sanitizedAppName == "" {
		return "", &ValidationError{Field: "app_name", Message: "app_name cannot be empty after sanitization"}
	}
	if sanitizedEnv == "" {
		return "", &ValidationError{Field: "environment", Message: "environment cannot be empty after sanitization"}
	}
	if sanitizedName == "" {
		return "", &ValidationError{Field: "name", Message: "name cannot be empty after sanitization"}
	}
	
	// Generate the resource name
	generatedName := GenerateResourceName(sanitizedAppName, sanitizedEnv, sanitizedName, resourceType)
	
	// Validate the generated name
	if err := ValidateResourceName(generatedName, resourceType); err != nil {
		return "", fmt.Errorf("generated name failed validation: %w", err)
	}
	
	return generatedName, nil
}
