// internal/resources/cloudwatch.go
package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"terraform-provider-conveyor-belt/internal/utils"
)

// CloudWatchManager handles CloudWatch Logs and Alarms operations
type CloudWatchManager struct {
	logsClient   *cloudwatchlogs.Client
	alarmsClient *cloudwatch.Client
	config       *DispatcherConfig
}

// NewCloudWatchManager creates a new CloudWatch manager
func NewCloudWatchManager(logsClient *cloudwatchlogs.Client, alarmsClient *cloudwatch.Client, config *DispatcherConfig) *CloudWatchManager {
	return &CloudWatchManager{
		logsClient:   logsClient,
		alarmsClient: alarmsClient,
		config:       config,
	}
}

// CreateLambdaLogGroup creates a CloudWatch log group for a Lambda function
func (cwm *CloudWatchManager) CreateLambdaLogGroup(ctx context.Context, lambdaFunctionName string) error {
	// Lambda log groups follow the pattern: /aws/lambda/<function-name>
	logGroupName := fmt.Sprintf("/aws/lambda/%s", lambdaFunctionName)

	return cwm.createLogGroup(ctx, logGroupName, 14, buildResourceTags(cwm.config, "CloudWatchLogGroup", logGroupName))
}

// CreateApiGatewayLogGroup creates a CloudWatch log group for an API Gateway
func (cwm *CloudWatchManager) CreateApiGatewayLogGroup(ctx context.Context, apiGatewayName string) (string, error) {
	// API Gateway log groups follow the pattern: /aws/apigateway/<api-name>
	logGroupName := fmt.Sprintf("/aws/apigateway/%s", apiGatewayName)

	err := cwm.createLogGroup(ctx, logGroupName, 14, buildResourceTags(cwm.config, "CloudWatchLogGroup", logGroupName))

	if err != nil {
		return "", err
	}

	// Return the ARN of the log group
	// ARN format: arn:aws:logs:region:account-id:log-group:log-group-name
	logGroupArn := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s",
		cwm.config.AwsRegion,
		cwm.config.AwsAccountId,
		logGroupName,
	)

	return logGroupArn, nil
}

// createLogGroup creates a CloudWatch log group with the specified retention and tags
func (cwm *CloudWatchManager) createLogGroup(ctx context.Context, logGroupName string, retentionDays int32, tags map[string]string) error {
	utils.Info(ctx, "Creating CloudWatch log group", map[string]interface{}{
		"log_group_name": logGroupName,
		"retention_days": retentionDays,
	})

	// Convert tags to CloudWatch format
	cwlTags := make(map[string]string)
	for k, v := range tags {
		cwlTags[k] = v
	}

	// Create the log group
	createLogGroupInput := &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(logGroupName),
		Tags:         cwlTags,
	}

	_, err := cwm.logsClient.CreateLogGroup(ctx, createLogGroupInput)
	if err != nil {
		// Check if log group already exists
		if strings.Contains(err.Error(), "ResourceAlreadyExistsException") {
			utils.Info(ctx, "CloudWatch log group already exists", map[string]interface{}{
				"log_group_name": logGroupName,
			})
			// Update retention policy if it exists
			_ = cwm.updateRetentionPolicy(ctx, logGroupName, retentionDays)
			return nil
		}
		return fmt.Errorf("failed to create CloudWatch log group: %w", err)
	}

	utils.Info(ctx, "Successfully created CloudWatch log group", map[string]interface{}{
		"log_group_name": logGroupName,
	})

	// Set retention policy
	err = cwm.updateRetentionPolicy(ctx, logGroupName, retentionDays)
	if err != nil {
		utils.Warn(ctx, "Failed to set retention policy, continuing anyway", map[string]interface{}{
			"log_group_name": logGroupName,
			"error":          err.Error(),
		})
	}

	return nil
}

// updateRetentionPolicy updates the retention policy for a log group
func (cwm *CloudWatchManager) updateRetentionPolicy(ctx context.Context, logGroupName string, retentionDays int32) error {
	putRetentionPolicyInput := &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(logGroupName),
		RetentionInDays: aws.Int32(retentionDays),
	}

	_, err := cwm.logsClient.PutRetentionPolicy(ctx, putRetentionPolicyInput)
	if err != nil {
		return fmt.Errorf("failed to set retention policy: %w", err)
	}

	utils.Debug(ctx, "Set CloudWatch log group retention policy", map[string]interface{}{
		"log_group_name": logGroupName,
		"retention_days": retentionDays,
	})

	return nil
}

// DeleteLambdaLogGroups deletes CloudWatch log groups for Lambda functions
func (cwm *CloudWatchManager) deleteLogGroups(ctx context.Context, names []string, prefix, resourceType string) error {
	for _, name := range names {
		logGroupName := fmt.Sprintf("%s%s", prefix, name)

		utils.Info(ctx, fmt.Sprintf("Deleting CloudWatch log group for %s", resourceType), map[string]interface{}{
			"log_group_name": logGroupName,
		})

		_, err := cwm.logsClient.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
			LogGroupName: aws.String(logGroupName),
		})
		if err != nil {
			if strings.Contains(err.Error(), "ResourceNotFoundException") {
				utils.Info(ctx, "CloudWatch log group does not exist, skipping deletion", map[string]interface{}{
					"log_group_name": logGroupName,
				})
				continue
			}

			utils.Warn(ctx, "Failed to delete CloudWatch log group", map[string]interface{}{
				"log_group_name": logGroupName,
				"error":          err.Error(),
			})
		} else {
			utils.Info(ctx, "Successfully deleted CloudWatch log group", map[string]interface{}{
				"log_group_name": logGroupName,
			})
		}
	}

	return nil
}

func (cwm *CloudWatchManager) DeleteLambdaLogGroups(ctx context.Context, lambdaFunctionNames []string) error {
	return cwm.deleteLogGroups(ctx, lambdaFunctionNames, "/aws/lambda/", "Lambda")
}

// DeleteApiGatewayLogGroups deletes CloudWatch log groups for API Gateways
func (cwm *CloudWatchManager) DeleteApiGatewayLogGroups(ctx context.Context, apiGatewayNames []string) error {
	return cwm.deleteLogGroups(ctx, apiGatewayNames, "/aws/apigateway/", "API Gateway")
}

// DeleteLambdaLogGroup deletes a CloudWatch log group for a specific Lambda function
func (cwm *CloudWatchManager) DeleteLambdaLogGroup(ctx context.Context, functionName string) error {
	logGroupName := fmt.Sprintf("/aws/lambda/%s", functionName)

	utils.Info(ctx, "Deleting CloudWatch log group for Lambda", map[string]interface{}{
		"log_group_name": logGroupName,
		"function_name":  functionName,
	})

	deleteInput := &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	}

	_, err := cwm.logsClient.DeleteLogGroup(ctx, deleteInput)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			utils.Info(ctx, "CloudWatch log group does not exist, skipping deletion", map[string]interface{}{
				"log_group_name": logGroupName,
			})
			return nil
		}
		return fmt.Errorf("failed to delete Lambda log group: %w", err)
	}

	utils.Info(ctx, "Successfully deleted CloudWatch log group", map[string]interface{}{
		"log_group_name": logGroupName,
	})

	return nil
}

// ===============================================
// CloudWatch Alarms Management
// ===============================================

// CreateOrUpdateLambdaAlarms creates or updates CloudWatch Alarms for a Lambda function
func (cwm *CloudWatchManager) CreateOrUpdateLambdaAlarms(ctx context.Context, lambdaFunctionName string, lambdaName string) error {
	if cwm.config.AlarmConfig == nil {
		utils.Info(ctx, "Alarms not configured (alarm_config is null), skipping alarm creation", map[string]interface{}{
			"function_name": lambdaFunctionName,
			"lambda":        lambdaName,
		})
		return nil
	}

	if !cwm.config.AlarmConfig.Enabled {
		utils.Info(ctx, "Alarms disabled (alarm_config.enabled = false), skipping alarm creation", map[string]interface{}{
			"function_name": lambdaFunctionName,
			"lambda":        lambdaName,
			"enabled":       cwm.config.AlarmConfig.Enabled,
		})
		return nil
	}

	if cwm.config.AlarmConfig.SnsTopicArn == "" {
		return fmt.Errorf("sns_topic_arn is required when alarms are enabled")
	}

	utils.Info(ctx, "Creating CloudWatch alarms for Lambda", map[string]interface{}{
		"function_name": lambdaFunctionName,
		"lambda":        lambdaName,
	})

	// Get config for this lambda (applying overrides if present)
	alarmConfig := cwm.getAlarmConfigForAction(lambdaName)

	// Create error alarm (if enabled)
	if alarmConfig.ErrorEnabled {
		if err := cwm.createErrorAlarm(ctx, lambdaFunctionName, lambdaName, alarmConfig); err != nil {
			utils.Warn(ctx, "Failed to create error alarm", map[string]interface{}{
				"lambda": lambdaFunctionName,
				"error":  err.Error(),
			})
		}
	}

	// Create duration alarm (if enabled)
	if alarmConfig.DurationEnabled {
		if err := cwm.createDurationAlarm(ctx, lambdaFunctionName, lambdaName, alarmConfig); err != nil {
			utils.Warn(ctx, "Failed to create duration alarm", map[string]interface{}{
				"lambda": lambdaFunctionName,
				"error":  err.Error(),
			})
		}
	}

	// Create throttle alarm (if enabled)
	if alarmConfig.ThrottleEnabled {
		if err := cwm.createThrottleAlarm(ctx, lambdaFunctionName, lambdaName, alarmConfig); err != nil {
			utils.Warn(ctx, "Failed to create throttle alarm", map[string]interface{}{
				"lambda": lambdaFunctionName,
				"error":  err.Error(),
			})
		}
	}

	// Create invocations alarm (if enabled)
	if alarmConfig.InvocationsEnabled {
		if err := cwm.createInvocationsAlarm(ctx, lambdaFunctionName, lambdaName, alarmConfig); err != nil {
			utils.Warn(ctx, "Failed to create invocations alarm", map[string]interface{}{
				"lambda": lambdaFunctionName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}

// getAlarmConfigForAction returns alarm config with lambda-specific overrides applied
func (cwm *CloudWatchManager) getAlarmConfigForAction(lambdaName string) *AlarmConfig {
	config := &AlarmConfig{
		Enabled:                      cwm.config.AlarmConfig.Enabled,
		SnsTopicArn:                  cwm.config.AlarmConfig.SnsTopicArn,
		ErrorEnabled:                 cwm.config.AlarmConfig.ErrorEnabled,
		ErrorThreshold:               cwm.config.AlarmConfig.ErrorThreshold,
		ErrorPeriod:                  cwm.config.AlarmConfig.ErrorPeriod,
		ErrorEvaluationPeriods:       cwm.config.AlarmConfig.ErrorEvaluationPeriods,
		ErrorStatistic:               cwm.config.AlarmConfig.ErrorStatistic,
		DurationEnabled:              cwm.config.AlarmConfig.DurationEnabled,
		DurationThreshold:            cwm.config.AlarmConfig.DurationThreshold,
		DurationPeriod:               cwm.config.AlarmConfig.DurationPeriod,
		DurationEvaluationPeriods:    cwm.config.AlarmConfig.DurationEvaluationPeriods,
		DurationStatistic:            cwm.config.AlarmConfig.DurationStatistic,
		ThrottleEnabled:              cwm.config.AlarmConfig.ThrottleEnabled,
		ThrottleThreshold:            cwm.config.AlarmConfig.ThrottleThreshold,
		ThrottlePeriod:               cwm.config.AlarmConfig.ThrottlePeriod,
		ThrottleEvaluationPeriods:    cwm.config.AlarmConfig.ThrottleEvaluationPeriods,
		ThrottleStatistic:            cwm.config.AlarmConfig.ThrottleStatistic,
		InvocationsEnabled:           cwm.config.AlarmConfig.InvocationsEnabled,
		InvocationsThreshold:         cwm.config.AlarmConfig.InvocationsThreshold,
		InvocationsPeriod:            cwm.config.AlarmConfig.InvocationsPeriod,
		InvocationsEvaluationPeriods: cwm.config.AlarmConfig.InvocationsEvaluationPeriods,
		InvocationsStatistic:         cwm.config.AlarmConfig.InvocationsStatistic,
	}

	// Apply lambda-specific overrides if present
	if override, ok := cwm.config.AlarmConfig.LambdaOverrides[lambdaName]; ok {
		// Error alarm overrides
		if override.ErrorEnabled != nil {
			config.ErrorEnabled = *override.ErrorEnabled
		}
		if override.ErrorThreshold != nil {
			config.ErrorThreshold = *override.ErrorThreshold
		}
		if override.ErrorPeriod != nil {
			config.ErrorPeriod = *override.ErrorPeriod
		}
		if override.ErrorEvaluationPeriods != nil {
			config.ErrorEvaluationPeriods = *override.ErrorEvaluationPeriods
		}

		// Duration alarm overrides
		if override.DurationEnabled != nil {
			config.DurationEnabled = *override.DurationEnabled
		}
		if override.DurationThreshold != nil {
			config.DurationThreshold = *override.DurationThreshold
		}
		if override.DurationPeriod != nil {
			config.DurationPeriod = *override.DurationPeriod
		}
		if override.DurationEvaluationPeriods != nil {
			config.DurationEvaluationPeriods = *override.DurationEvaluationPeriods
		}

		// Throttle alarm overrides
		if override.ThrottleEnabled != nil {
			config.ThrottleEnabled = *override.ThrottleEnabled
		}
		if override.ThrottleThreshold != nil {
			config.ThrottleThreshold = *override.ThrottleThreshold
		}
		if override.ThrottlePeriod != nil {
			config.ThrottlePeriod = *override.ThrottlePeriod
		}
		if override.ThrottleEvaluationPeriods != nil {
			config.ThrottleEvaluationPeriods = *override.ThrottleEvaluationPeriods
		}

		// Invocations alarm overrides
		if override.InvocationsEnabled != nil {
			config.InvocationsEnabled = *override.InvocationsEnabled
		}
		if override.InvocationsThreshold != nil {
			config.InvocationsThreshold = *override.InvocationsThreshold
		}
		if override.InvocationsPeriod != nil {
			config.InvocationsPeriod = *override.InvocationsPeriod
		}
		if override.InvocationsEvaluationPeriods != nil {
			config.InvocationsEvaluationPeriods = *override.InvocationsEvaluationPeriods
		}
	}

	return config
}

// alarmSpec defines the parameters that vary between alarm types.
type alarmSpec struct {
	suffix      string
	metricName  string
	threshold   int64
	period      int64
	evalPeriods int64
	statistic   string
	description string
}

// createLambdaAlarm creates a single CloudWatch Alarm for a Lambda metric.
func (cwm *CloudWatchManager) createLambdaAlarm(ctx context.Context, lambdaFunctionName string, spec alarmSpec) error {
	alarmName := fmt.Sprintf("%s-%s", lambdaFunctionName, spec.suffix)

	_, err := cwm.alarmsClient.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(int32(spec.evalPeriods)),
		MetricName:         aws.String(spec.metricName),
		Namespace:          aws.String("AWS/Lambda"),
		Period:             aws.Int32(int32(spec.period)),
		Statistic:          cwtypes.Statistic(spec.statistic),
		Threshold:          aws.Float64(float64(spec.threshold)),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{cwm.config.AlarmConfig.SnsTopicArn},
		AlarmDescription:   aws.String(spec.description),
		Dimensions: []cwtypes.Dimension{
			{
				Name:  aws.String("FunctionName"),
				Value: aws.String(lambdaFunctionName),
			},
		},
		Tags: convertToCloudWatchTags(buildResourceTags(cwm.config, "CloudWatchAlarm", alarmName)),
	})
	if err != nil {
		return fmt.Errorf("failed to create %s alarm: %w", spec.suffix, err)
	}
	return nil
}

func (cwm *CloudWatchManager) createErrorAlarm(ctx context.Context, lambdaFunctionName string, lambdaName string, config *AlarmConfig) error {
	return cwm.createLambdaAlarm(ctx, lambdaFunctionName, alarmSpec{
		suffix:      "errors",
		metricName:  "Errors",
		threshold:   config.ErrorThreshold,
		period:      config.ErrorPeriod,
		evalPeriods: config.ErrorEvaluationPeriods,
		statistic:   config.ErrorStatistic,
		description: fmt.Sprintf("Alert when %s Lambda has >%d errors in %d seconds", lambdaName, config.ErrorThreshold, config.ErrorPeriod),
	})
}

func (cwm *CloudWatchManager) createDurationAlarm(ctx context.Context, lambdaFunctionName string, lambdaName string, config *AlarmConfig) error {
	return cwm.createLambdaAlarm(ctx, lambdaFunctionName, alarmSpec{
		suffix:      "duration",
		metricName:  "Duration",
		threshold:   config.DurationThreshold,
		period:      config.DurationPeriod,
		evalPeriods: config.DurationEvaluationPeriods,
		statistic:   config.DurationStatistic,
		description: fmt.Sprintf("Alert when %s Lambda duration >%dms", lambdaName, config.DurationThreshold),
	})
}

func (cwm *CloudWatchManager) createThrottleAlarm(ctx context.Context, lambdaFunctionName string, lambdaName string, config *AlarmConfig) error {
	return cwm.createLambdaAlarm(ctx, lambdaFunctionName, alarmSpec{
		suffix:      "throttles",
		metricName:  "Throttles",
		threshold:   config.ThrottleThreshold,
		period:      config.ThrottlePeriod,
		evalPeriods: config.ThrottleEvaluationPeriods,
		statistic:   config.ThrottleStatistic,
		description: fmt.Sprintf("Alert when %s Lambda has >%d throttles", lambdaName, config.ThrottleThreshold),
	})
}

func (cwm *CloudWatchManager) createInvocationsAlarm(ctx context.Context, lambdaFunctionName string, lambdaName string, config *AlarmConfig) error {
	return cwm.createLambdaAlarm(ctx, lambdaFunctionName, alarmSpec{
		suffix:      "invocations",
		metricName:  "Invocations",
		threshold:   config.InvocationsThreshold,
		period:      config.InvocationsPeriod,
		evalPeriods: config.InvocationsEvaluationPeriods,
		statistic:   config.InvocationsStatistic,
		description: fmt.Sprintf("Alert when %s Lambda has >%d invocations in %d seconds", lambdaName, config.InvocationsThreshold, config.InvocationsPeriod),
	})
}

// DisableLambdaAlarms disables (but doesn't delete) all alarms for a Lambda function
func (cwm *CloudWatchManager) DisableLambdaAlarms(ctx context.Context, lambdaFunctionName string) error {
	alarmNames := []string{
		fmt.Sprintf("%s-errors", lambdaFunctionName),
		fmt.Sprintf("%s-duration", lambdaFunctionName),
		fmt.Sprintf("%s-throttles", lambdaFunctionName),
		fmt.Sprintf("%s-invocations", lambdaFunctionName),
	}

	for _, alarmName := range alarmNames {
		_, err := cwm.alarmsClient.DisableAlarmActions(ctx, &cloudwatch.DisableAlarmActionsInput{
			AlarmNames: []string{alarmName},
		})
		if err != nil {
			// Ignore errors for non-existent alarms
			if !strings.Contains(err.Error(), "ResourceNotFound") {
				utils.Warn(ctx, "Failed to disable alarm", map[string]interface{}{
					"alarm_name": alarmName,
					"error":      err.Error(),
				})
			}
		} else {
			utils.Info(ctx, "Disabled alarm", map[string]interface{}{
				"alarm_name": alarmName,
			})
		}
	}

	return nil
}

// DeleteLambdaAlarms deletes all alarms for a Lambda function
func (cwm *CloudWatchManager) DeleteLambdaAlarms(ctx context.Context, lambdaFunctionName string) error {
	alarmNames := []string{
		fmt.Sprintf("%s-errors", lambdaFunctionName),
		fmt.Sprintf("%s-duration", lambdaFunctionName),
		fmt.Sprintf("%s-throttles", lambdaFunctionName),
		fmt.Sprintf("%s-invocations", lambdaFunctionName),
	}

	_, err := cwm.alarmsClient.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{
		AlarmNames: alarmNames,
	})
	if err != nil {
		return fmt.Errorf("failed to delete alarms: %w", err)
	}

	utils.Info(ctx, "Deleted Lambda alarms", map[string]interface{}{
		"lambda":      lambdaFunctionName,
		"alarm_count": len(alarmNames),
	})

	return nil
}

// DeleteApiGatewayLogGroup deletes a CloudWatch log group for a specific API Gateway
func (cwm *CloudWatchManager) DeleteApiGatewayLogGroup(ctx context.Context, apiName string) error {
	logGroupName := fmt.Sprintf("/aws/apigateway/%s", apiName)

	utils.Info(ctx, "Deleting CloudWatch log group for API Gateway", map[string]interface{}{
		"log_group_name": logGroupName,
		"api_name":       apiName,
	})

	deleteInput := &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	}

	_, err := cwm.logsClient.DeleteLogGroup(ctx, deleteInput)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			utils.Info(ctx, "CloudWatch log group does not exist, skipping deletion", map[string]interface{}{
				"log_group_name": logGroupName,
			})
			return nil
		}
		return fmt.Errorf("failed to delete API Gateway log group: %w", err)
	}

	utils.Info(ctx, "Successfully deleted CloudWatch log group", map[string]interface{}{
		"log_group_name": logGroupName,
	})

	return nil
}

// convertToCloudWatchTags converts a map of tags to CloudWatch tag format
func convertToCloudWatchTags(tags map[string]string) []cwtypes.Tag {
	cwTags := make([]cwtypes.Tag, 0, len(tags))
	for key, value := range tags {
		cwTags = append(cwTags, cwtypes.Tag{
			Key:   aws.String(key),
			Value: aws.String(value),
		})
	}
	return cwTags
}
