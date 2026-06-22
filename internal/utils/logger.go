// internal/utils/logger.go
package utils

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const logPrefix = "[CONVEYOR-BELT]"

// Info logs an info message with CONVEYOR prefix
func Info(ctx context.Context, msg string, additionalFields ...map[string]interface{}) {
	tflog.Info(ctx, fmt.Sprintf("%s %s", logPrefix, msg), mergeFields(additionalFields)...)
}

// Debug logs a debug message with CONVEYOR prefix
func Debug(ctx context.Context, msg string, additionalFields ...map[string]interface{}) {
	tflog.Debug(ctx, fmt.Sprintf("%s %s", logPrefix, msg), mergeFields(additionalFields)...)
}

// Warn logs a warning message with CONVEYOR prefix
func Warn(ctx context.Context, msg string, additionalFields ...map[string]interface{}) {
	tflog.Warn(ctx, fmt.Sprintf("%s %s", logPrefix, msg), mergeFields(additionalFields)...)
}

// Error logs an error message with CONVEYOR prefix
func Error(ctx context.Context, msg string, additionalFields ...map[string]interface{}) {
	tflog.Error(ctx, fmt.Sprintf("%s %s", logPrefix, msg), mergeFields(additionalFields)...)
}

// Trace logs a trace message with CONVEYOR prefix
func Trace(ctx context.Context, msg string, additionalFields ...map[string]interface{}) {
	tflog.Trace(ctx, fmt.Sprintf("%s %s", logPrefix, msg), mergeFields(additionalFields)...)
}

// mergeFields is a helper to handle optional additional fields
func mergeFields(additionalFields []map[string]interface{}) []map[string]interface{} {
	return additionalFields
}
