// internal/resources/parallel_manager.go
package resources

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// ParallelManager handles concurrent Lambda and Gateway operations
type ParallelManager struct {
	concurrency int
	clients     *ResourceClients
	config      *DispatcherConfig
}

// LambdaResult represents the result of a Lambda operation
type LambdaResult struct {
	Action  string
	ARN     string
	Success bool
	Error   error
}

// LambdaUpdateType indicates what kind of update is needed
type LambdaUpdateType string

const (
	// LambdaUpdateTypeNone indicates no update needed
	LambdaUpdateTypeNone LambdaUpdateType = "none"
	// LambdaUpdateTypeSource indicates only source code changed
	LambdaUpdateTypeSource LambdaUpdateType = "source"
	// LambdaUpdateTypeConfig indicates only configuration changed
	LambdaUpdateTypeConfig LambdaUpdateType = "config"
	// LambdaUpdateTypeBoth indicates both source and config changed
	LambdaUpdateTypeBoth LambdaUpdateType = "both"
)

// LambdaUpdateTask represents a Lambda that needs updating with its update type
type LambdaUpdateTask struct {
	Action     string
	UpdateType LambdaUpdateType
	ARN        string // Existing ARN from state, avoids extra GetFunction call
}

// GatewayResult represents the result of a Gateway operation
type GatewayResult struct {
	Gateway string
	ID      string
	URL     string
	Success bool
	Error   error
}

// NewParallelManager creates a new ParallelManager
func NewParallelManager(concurrency int, clients *ResourceClients, config *DispatcherConfig) *ParallelManager {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	return &ParallelManager{
		concurrency: concurrency,
		clients:     clients,
		config:      config,
	}
}

// CreateLambdasInParallel creates multiple Lambda functions concurrently
// runLambdaOpsInParallel runs an operation for each lambda name with semaphore-based concurrency.
func (pm *ParallelManager) runLambdaOpsInParallel(lambdas []string, op func(string) LambdaResult) []LambdaResult {
	results := make([]LambdaResult, 0, len(lambdas))
	resultChan := make(chan LambdaResult, len(lambdas))

	sem := make(chan struct{}, pm.concurrency)

	var wg sync.WaitGroup
	for _, lambdaName := range lambdas {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resultChan <- op(name)
		}(lambdaName)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}
	return results
}

func (pm *ParallelManager) CreateLambdasInParallel(
	ctx context.Context,
	lambdas []string,
	buildResults map[string]*BuildResult,
	routes []utils.Route,
	lambdaConfig map[string]interface{},
	iamManager *IAMManager,
	cloudWatchManager *CloudWatchManager,
) []LambdaResult {
	return pm.runLambdaOpsInParallel(lambdas, func(name string) LambdaResult {
		return pm.createSingleLambda(ctx, name, buildResults[name], routes, lambdaConfig, iamManager, cloudWatchManager)
	})
}

// createSingleLambda creates a single Lambda function with all associated resources
func (pm *ParallelManager) createSingleLambda(
	ctx context.Context,
	lambdaName string,
	buildResult *BuildResult,
	routes []utils.Route,
	lambdaConfig map[string]interface{},
	iamManager *IAMManager,
	cloudWatchManager *CloudWatchManager,
) LambdaResult {
	result := LambdaResult{Action: lambdaName}

	if buildResult == nil || buildResult.Error != nil {
		errMsg := "no build result"
		if buildResult != nil && buildResult.Error != nil {
			errMsg = buildResult.Error.Error()
		}
		result.Error = fmt.Errorf("build failed: %s", errMsg)
		return result
	}

	functionName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, lambdaName)
	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", pm.config.AppName, pm.config.Environment, lambdaName)

	utils.Info(ctx, "Creating Lambda function", map[string]interface{}{
		"lambda":        lambdaName,
		"function_name": functionName,
	})

	// Step 1: Create CloudWatch log group
	if err := cloudWatchManager.CreateLambdaLogGroup(ctx, functionName); err != nil {
		utils.Warn(ctx, "Failed to create CloudWatch log group", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
		// Continue - Lambda will create it automatically
	}

	// Step 2: Create IAM execution role
	roleArn, err := iamManager.CreateLambdaExecutionRole(ctx, roleName, lambdaName)
	if err != nil {
		result.Error = fmt.Errorf("failed to create IAM role: %w", err)
		return result
	}

	// Attach shared IAM policies
	if err := iamManager.AttachSharedIamPolicies(ctx, roleName); err != nil {
		result.Error = fmt.Errorf("failed to attach shared IAM policies to role %s: %w", roleName, err)
		return result
	}

	// Attach VPC execution policy if this lambda has vpc_config
	if extractVpcConfig(lambdaConfig, lambdaName) != nil {
		if err := iamManager.AttachVpcExecutionPolicy(ctx, roleName); err != nil {
			utils.Warn(ctx, "Failed to attach VPC execution policy", map[string]interface{}{
				"role_name": roleName,
				"error":     err.Error(),
			})
		}
	}

	// Step 3: Create DynamoDB policies based on routes AND lambda_config.dynamodb_tables
	// Always call CreateDynamoDBPoliciesForAction - it handles both route-based tables
	// and lambda_config.{action}.dynamodb_tables for standalone lambdas
	lambdaRoutes := filterRoutesByLambda(routes, lambdaName)
	if err := iamManager.CreateDynamoDBPoliciesForAction(ctx, lambdaName, lambdaRoutes, roleName); err != nil {
		utils.Warn(ctx, "Failed to create DynamoDB policies", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
	}

	// Create S3 policies from lambda_config.{action}.s3_buckets
	if err := iamManager.CreateS3PoliciesForAction(ctx, lambdaName, roleName); err != nil {
		utils.Warn(ctx, "Failed to create S3 policies", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
	}

	// Create SES policies from lambda_config.{action}.ses_emails
	if err := iamManager.CreateSESPoliciesForAction(ctx, lambdaName, roleName); err != nil {
		utils.Warn(ctx, "Failed to create SES policies", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
	}

	// Create SQS policies from lambda_config.{action}.sqs_triggers
	if err := iamManager.CreateSQSPoliciesForAction(ctx, lambdaName, roleName); err != nil {
		utils.Warn(ctx, "Failed to create SQS policies", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
	}

	// Step 4: Create Lambda function
	envVars := pm.buildEnvVars(lambdaName, routes, lambdaConfig)
	timeout, memory := pm.getTimeoutAndMemory(lambdaName, lambdaConfig)
	vpcConfig := extractVpcConfig(lambdaConfig, lambdaName)

	lambdaArn, err := pm.createLambdaFunction(ctx, functionName, roleArn, lambdaName, buildResult.ZipData, envVars, timeout, memory, vpcConfig)
	if err != nil {
		// Rollback: delete IAM role
		iamManager.DeleteLambdaRole(ctx, roleName, lambdaName)
		cloudWatchManager.DeleteLambdaLogGroup(ctx, functionName)
		result.Error = fmt.Errorf("failed to create Lambda function: %w", err)
		return result
	}

	// Step 5: Create CloudWatch alarms
	if err := cloudWatchManager.CreateOrUpdateLambdaAlarms(ctx, functionName, lambdaName); err != nil {
		utils.Warn(ctx, "Failed to create CloudWatch alarms", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
	}

	// Step 6: Set up SNS/SQS triggers if configured
	pm.setupTriggers(ctx, lambdaName, lambdaArn, lambdaConfig)

	result.ARN = lambdaArn
	result.Success = true

	utils.Info(ctx, "Successfully created Lambda function", map[string]interface{}{
		"lambda": lambdaName,
		"arn":    lambdaArn,
	})

	return result
}

// createLambdaFunction creates the Lambda function in AWS
func (pm *ParallelManager) createLambdaFunction(
	ctx context.Context,
	functionName, roleArn, lambdaName string,
	zipData []byte,
	envVars map[string]string,
	timeout, memory int64,
	vpcConfig *VpcConfig,
) (string, error) {
	createInput := &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Runtime:      lambdaTypes.RuntimeRuby34,
		Role:         aws.String(roleArn),
		Handler:      aws.String(fmt.Sprintf("%s.lambda_handler", lambdaName)),
		Code: &lambdaTypes.FunctionCode{
			ZipFile: zipData,
		},
		Description: aws.String(fmt.Sprintf("Lambda function for %s in %s-%s", lambdaName, pm.config.AppName, pm.config.Environment)),
		Environment: &lambdaTypes.Environment{
			Variables: envVars,
		},
		Tags:       buildResourceTags(pm.config, "Lambda", functionName),
		Timeout:    aws.Int32(int32(timeout)),
		MemorySize: aws.Int32(int32(memory)),
	}

	// Attach Lambda Layers if specified
	if len(pm.config.LambdaLayerArns) > 0 {
		createInput.Layers = pm.config.LambdaLayerArns
	}

	// Attach VPC configuration if specified
	if vpcConfig != nil {
		createInput.VpcConfig = &lambdaTypes.VpcConfig{
			SubnetIds:        vpcConfig.SubnetIds,
			SecurityGroupIds: vpcConfig.SecurityGroupIds,
		}
	}

	// Retry logic for IAM role propagation
	var createErr error
	for attempt := 0; attempt < 10; attempt++ {
		output, err := pm.clients.Lambda.CreateFunction(ctx, createInput)
		if err == nil {
			return *output.FunctionArn, nil
		}

		createErr = err

		// Check if function already exists
		if strings.Contains(err.Error(), "ResourceConflictException") {
			getOutput, getErr := pm.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
				FunctionName: aws.String(functionName),
			})
			if getErr == nil {
				return *getOutput.Configuration.FunctionArn, nil
			}
			return "", fmt.Errorf("function exists but failed to get ARN: %w", getErr)
		}

		// Check if IAM role propagation error (includes KMS grant failures)
		if strings.Contains(err.Error(), "cannot be assumed by Lambda") ||
			strings.Contains(err.Error(), "KMS key is invalid for CreateGrant") ||
			strings.Contains(err.Error(), "ARN does not refer to a valid principal") {
			utils.Info(ctx, "IAM role not yet propagated, retrying...", map[string]interface{}{
				"attempt":      attempt + 1,
				"error_detail": err.Error(),
			})
			time.Sleep(time.Duration(attempt+1) * time.Second) // Exponential backoff
			continue
		}

		// Signature expired — large zip upload exceeded the 5-minute window
		if strings.Contains(err.Error(), "InvalidSignatureException") ||
			strings.Contains(err.Error(), "Signature expired") {
			utils.Warn(ctx, "Signature expired during code upload, retrying", map[string]interface{}{
				"lambda":   lambdaName,
				"attempt":  attempt + 1,
				"zip_size": len(zipData),
			})
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}

		return "", err
	}

	return "", fmt.Errorf("failed to create Lambda function after retries: %w", createErr)
}


// UpdateLambdasInParallel updates multiple Lambda functions concurrently
// It accepts LambdaUpdateTask structs that specify what type of update is needed
func (pm *ParallelManager) UpdateLambdasInParallel(
	ctx context.Context,
	updateTasks []LambdaUpdateTask,
	buildResults map[string]*BuildResult,
	routes []utils.Route,
	lambdaConfig map[string]interface{},
	iamManager *IAMManager,
	cloudWatchManager *CloudWatchManager,
) []LambdaResult {
	startTime := time.Now()
	
	// Log parallel execution start with unique prefix for grep
	utils.Info(ctx, "[PARALLEL_UPDATE] Starting parallel Lambda updates", map[string]interface{}{
		"total_lambdas": len(updateTasks),
		"concurrency":   pm.concurrency,
	})
	
	// Log which lambdas will be updated
	lambdaNames := make([]string, len(updateTasks))
	for i, t := range updateTasks {
		lambdaNames[i] = t.Action
	}
	utils.Info(ctx, "[PARALLEL_UPDATE] Lambdas queued for update", map[string]interface{}{
		"lambdas": strings.Join(lambdaNames, ", "),
	})

	results := make([]LambdaResult, 0, len(updateTasks))
	resultChan := make(chan LambdaResult, len(updateTasks))

	sem := make(chan struct{}, pm.concurrency)
	
	// Track active goroutines for logging
	var activeCount int32
	var activeCountMu sync.Mutex

	var wg sync.WaitGroup
	for _, task := range updateTasks {
		wg.Add(1)
		go func(t LambdaUpdateTask) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			
			// Track active count
			activeCountMu.Lock()
			activeCount++
			currentActive := activeCount
			activeCountMu.Unlock()
			
			taskStart := time.Now()
			utils.Info(ctx, "[PARALLEL_UPDATE] Lambda update STARTED", map[string]interface{}{
				"lambda":          t.Action,
				"update_type":     string(t.UpdateType),
				"active_workers":  currentActive,
				"max_concurrency": pm.concurrency,
			})
			
			defer func() {
				<-sem
				activeCountMu.Lock()
				activeCount--
				activeCountMu.Unlock()
			}()

			result := pm.updateSingleLambda(ctx, t.Action, t.UpdateType, t.ARN, buildResults[t.Action], routes, lambdaConfig, iamManager, cloudWatchManager)
			
			utils.Info(ctx, "[PARALLEL_UPDATE] Lambda update COMPLETED", map[string]interface{}{
				"lambda":      t.Action,
				"success":     result.Success,
				"duration_ms": time.Since(taskStart).Milliseconds(),
			})
			
			resultChan <- result
		}(task)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}
	
	// Log completion summary
	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}
	
	utils.Info(ctx, "[PARALLEL_UPDATE] Parallel Lambda updates FINISHED", map[string]interface{}{
		"total_lambdas":    len(updateTasks),
		"successful":       successCount,
		"failed":           failCount,
		"total_duration_ms": time.Since(startTime).Milliseconds(),
		"concurrency_used": pm.concurrency,
	})

	return results
}

// updateSingleLambda updates a single Lambda function based on the update type
func (pm *ParallelManager) updateSingleLambda(
	ctx context.Context,
	lambdaName string,
	updateType LambdaUpdateType,
	existingARN string,
	buildResult *BuildResult,
	routes []utils.Route,
	lambdaConfig map[string]interface{},
	iamManager *IAMManager,
	cloudWatchManager *CloudWatchManager,
) LambdaResult {
	result := LambdaResult{Action: lambdaName}

	functionName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, lambdaName)

	utils.Info(ctx, "Updating Lambda function", map[string]interface{}{
		"lambda":        lambdaName,
		"function_name": functionName,
		"update_type":   string(updateType),
	})

	// Update code if source changed (or both)
	if updateType == LambdaUpdateTypeSource || updateType == LambdaUpdateTypeBoth {
		if buildResult != nil && buildResult.Error == nil && buildResult.ZipData != nil {
			// Wait for any pending updates to complete before updating code
			if err := pm.waitForLambdaReady(ctx, functionName); err != nil {
				utils.Warn(ctx, "Lambda not ready for code update", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}

			// Retry on InvalidSignatureException — large zip uploads can exceed
			// the 5-minute signature validity window on slow connections.
			var updateCodeErr error
			for attempt := 0; attempt < 3; attempt++ {
				_, updateCodeErr = pm.clients.Lambda.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
					FunctionName: aws.String(functionName),
					ZipFile:      buildResult.ZipData,
				})
				if updateCodeErr == nil {
					break
				}
				if strings.Contains(updateCodeErr.Error(), "InvalidSignatureException") ||
					strings.Contains(updateCodeErr.Error(), "Signature expired") {
					utils.Warn(ctx, "Signature expired during code upload, retrying", map[string]interface{}{
						"lambda":   lambdaName,
						"attempt":  attempt + 1,
						"zip_size": len(buildResult.ZipData),
					})
					time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
					continue
				}
				break
			}
			if updateCodeErr != nil {
				result.Error = fmt.Errorf("failed to update Lambda code: %w", updateCodeErr)
				return result
			}
			utils.Info(ctx, "Updated Lambda code", map[string]interface{}{
				"lambda": lambdaName,
			})
		} else if updateType == LambdaUpdateTypeSource || updateType == LambdaUpdateTypeBoth {
			// Source update was requested but no build result available
			utils.Warn(ctx, "Source update requested but no build result available", map[string]interface{}{
				"lambda": lambdaName,
			})
		}
	}

	// Update configuration if config changed (or both)
	if updateType == LambdaUpdateTypeConfig || updateType == LambdaUpdateTypeBoth {
		// Validate IAM policies BEFORE updating Lambda config to avoid partial updates
		if iamManager != nil {
			roleName := fmt.Sprintf("%s-%s-%s-lambda-role", pm.config.AppName, pm.config.Environment, lambdaName)

			// Detach stale policies first to stay within the 10-policy-per-role AWS limit
			if err := iamManager.ReconcileSharedIamPolicies(ctx, roleName); err != nil {
				utils.Warn(ctx, "Failed to reconcile shared IAM policies before attach", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}

			if err := iamManager.AttachSharedIamPolicies(ctx, roleName); err != nil {
				result.Error = fmt.Errorf("failed to attach shared IAM policies during update for %s: %w", lambdaName, err)
				return result
			}
		}

		// Wait for any pending updates to complete before updating config
		if err := pm.waitForLambdaReady(ctx, functionName); err != nil {
			utils.Warn(ctx, "Lambda not ready for config update", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}

		envVars := pm.buildEnvVars(lambdaName, routes, lambdaConfig)
		timeout, memory := pm.getTimeoutAndMemory(lambdaName, lambdaConfig)
		vpcConfig := extractVpcConfig(lambdaConfig, lambdaName)

		updateConfigInput := &lambda.UpdateFunctionConfigurationInput{
			FunctionName: aws.String(functionName),
			Environment: &lambdaTypes.Environment{
				Variables: envVars,
			},
			Timeout:    aws.Int32(int32(timeout)),
			MemorySize: aws.Int32(int32(memory)),
		}

		if len(pm.config.LambdaLayerArns) > 0 {
			updateConfigInput.Layers = pm.config.LambdaLayerArns
		}

		if vpcConfig != nil {
			updateConfigInput.VpcConfig = &lambdaTypes.VpcConfig{
				SubnetIds:        vpcConfig.SubnetIds,
				SecurityGroupIds: vpcConfig.SecurityGroupIds,
			}
		}

		_, err := pm.clients.Lambda.UpdateFunctionConfiguration(ctx, updateConfigInput)
		if err != nil {
			result.Error = fmt.Errorf("failed to update Lambda configuration for %s: %w", lambdaName, err)
			return result
		}
		utils.Info(ctx, "Updated Lambda configuration", map[string]interface{}{
			"lambda":        lambdaName,
			"env_var_count": len(envVars),
		})

		// Update remaining IAM policies (DynamoDB, S3, SES) after config update
		if iamManager != nil {
			roleName := fmt.Sprintf("%s-%s-%s-lambda-role", pm.config.AppName, pm.config.Environment, lambdaName)
			lambdaRoutes := filterRoutesByLambda(routes, lambdaName)

			if err := iamManager.CreateDynamoDBPoliciesForAction(ctx, lambdaName, lambdaRoutes, roleName); err != nil {
				utils.Warn(ctx, "Failed to update DynamoDB policies", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}

			if err := iamManager.CreateS3PoliciesForAction(ctx, lambdaName, roleName); err != nil {
				utils.Warn(ctx, "Failed to update S3 policies", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}

			if err := iamManager.CreateSESPoliciesForAction(ctx, lambdaName, roleName); err != nil {
				utils.Warn(ctx, "Failed to update SES policies", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}

			if err := iamManager.CreateSQSPoliciesForAction(ctx, lambdaName, roleName); err != nil {
				utils.Warn(ctx, "Failed to update SQS policies", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			} else {
				utils.Info(ctx, "Updated IAM policies for Lambda", map[string]interface{}{
					"lambda": lambdaName,
				})
			}
		}

		// Reconcile triggers (SNS/SQS) when config changes
		// Use existing ARN from state instead of calling GetFunction
		lambdaArn := existingARN
		if lambdaArn == "" {
			// Fallback: get ARN from AWS if not provided
			if getOutput, err := pm.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
				FunctionName: aws.String(functionName),
			}); err == nil && getOutput.Configuration != nil {
				lambdaArn = aws.ToString(getOutput.Configuration.FunctionArn)
			}
		}

		if lambdaArn != "" {
			triggerManager := NewTriggerManager(pm.clients, pm.config)
			if err := triggerManager.ReconcileTriggers(ctx, lambdaName, lambdaArn, lambdaConfig); err != nil {
				utils.Warn(ctx, "Failed to reconcile triggers", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			}
		} else {
			utils.Warn(ctx, "Could not get Lambda ARN for trigger reconciliation", map[string]interface{}{
				"lambda": lambdaName,
			})
		}
	}

	// Update CloudWatch alarms when config changes
	if cloudWatchManager != nil && (updateType == LambdaUpdateTypeConfig || updateType == LambdaUpdateTypeBoth) {
		if err := cloudWatchManager.CreateOrUpdateLambdaAlarms(ctx, functionName, lambdaName); err != nil {
			utils.Warn(ctx, "Failed to update CloudWatch alarms", map[string]interface{}{
				"function_name": functionName,
				"error":         err.Error(),
			})
		}
	}

	// Use existing ARN from state instead of calling GetFunction
	result.ARN = existingARN
	if result.ARN == "" {
		// Fallback for safety
		if getOutput, err := pm.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
			FunctionName: aws.String(functionName),
		}); err == nil {
			result.ARN = *getOutput.Configuration.FunctionArn
		}
	}

	result.Success = true

	utils.Info(ctx, "Successfully updated Lambda function", map[string]interface{}{
		"lambda":      lambdaName,
		"update_type": string(updateType),
	})

	return result
}

// waitForLambdaReady waits for a Lambda function to be ready for updates
func (pm *ParallelManager) waitForLambdaReady(ctx context.Context, functionName string) error {
	for i := 0; i < 30; i++ {
		output, err := pm.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
			FunctionName: aws.String(functionName),
		})
		if err != nil {
			return err
		}

		if output.Configuration != nil {
			state := output.Configuration.State
			lastUpdateStatus := output.Configuration.LastUpdateStatus

			// Check if function is active and not being updated
			if state == lambdaTypes.StateActive &&
				(lastUpdateStatus == "" || lastUpdateStatus == lambdaTypes.LastUpdateStatusSuccessful) {
				return nil
			}

			// If update failed, return error
			if lastUpdateStatus == lambdaTypes.LastUpdateStatusFailed {
				return fmt.Errorf("previous update failed")
			}
		}

		// Wait before retrying
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	return fmt.Errorf("timeout waiting for Lambda to be ready")
}

// DeleteLambdasInParallel deletes multiple Lambda functions concurrently
func (pm *ParallelManager) DeleteLambdasInParallel(
	ctx context.Context,
	lambdas []string,
	iamManager *IAMManager,
	cloudWatchManager *CloudWatchManager,
) []LambdaResult {
	return pm.runLambdaOpsInParallel(lambdas, func(name string) LambdaResult {
		return pm.deleteSingleLambda(ctx, name, iamManager, cloudWatchManager)
	})
}

// deleteSingleLambda deletes a single Lambda function and associated resources
func (pm *ParallelManager) deleteSingleLambda(
	ctx context.Context,
	lambdaName string,
	iamManager *IAMManager,
	cloudWatchManager *CloudWatchManager,
) LambdaResult {
	result := LambdaResult{Action: lambdaName}

	functionName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, lambdaName)
	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", pm.config.AppName, pm.config.Environment, lambdaName)

	utils.Info(ctx, "Deleting Lambda function", map[string]interface{}{
		"lambda":        lambdaName,
		"function_name": functionName,
	})

	// Delete CloudWatch alarms
	if err := cloudWatchManager.DeleteLambdaAlarms(ctx, functionName); err != nil {
		utils.Warn(ctx, "Failed to delete CloudWatch alarms", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
	}

	// Delete CloudWatch log group
	if err := cloudWatchManager.DeleteLambdaLogGroup(ctx, functionName); err != nil {
		utils.Warn(ctx, "Failed to delete CloudWatch log group", map[string]interface{}{
			"function_name": functionName,
			"error":         err.Error(),
		})
	}

	// Clean up triggers (SNS/SQS) before deleting Lambda
	// Requirements: 5.3 - When a Lambda is deleted, clean up all associated triggers
	// Get the Lambda ARN first (needed for SNS trigger cleanup)
	lambdaArn := ""
	if getOutput, err := pm.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String(functionName),
	}); err == nil && getOutput.Configuration != nil {
		lambdaArn = aws.ToString(getOutput.Configuration.FunctionArn)
	}

	if lambdaArn != "" {
		triggerManager := NewTriggerManager(pm.clients, pm.config)
		if err := triggerManager.CleanupAllTriggers(ctx, lambdaName, lambdaArn); err != nil {
			utils.Warn(ctx, "Failed to clean up triggers", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
			// Don't fail the deletion - trigger cleanup is best-effort
		}
	}

	// Delete Lambda function
	_, err := pm.clients.Lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
		FunctionName: aws.String(functionName),
	})
	if err != nil && !strings.Contains(err.Error(), "ResourceNotFoundException") {
		result.Error = fmt.Errorf("failed to delete Lambda function: %w", err)
		return result
	}

	// Delete IAM role
	if err := iamManager.DeleteLambdaRole(ctx, roleName, lambdaName); err != nil {
		utils.Warn(ctx, "Failed to delete IAM role", map[string]interface{}{
			"role_name": roleName,
			"error":     err.Error(),
		})
	}

	result.Success = true

	utils.Info(ctx, "Successfully deleted Lambda function", map[string]interface{}{
		"lambda": lambdaName,
	})

	return result
}

// findExistingApiGateway looks for an existing API Gateway by name.
// Returns the API ID if found, empty string if not found.
// This prevents duplicate API Gateways during replace operations or retries.
func (pm *ParallelManager) findExistingApiGateway(ctx context.Context, apiName string) (string, error) {
	listInput := &apigateway.GetRestApisInput{
		Limit: aws.Int32(500),
	}

	listOutput, err := pm.clients.ApiGateway.GetRestApis(ctx, listInput)
	if err != nil {
		return "", fmt.Errorf("failed to list API Gateways: %w", err)
	}

	for _, api := range listOutput.Items {
		if api.Name != nil && *api.Name == apiName {
			return *api.Id, nil
		}
	}

	return "", nil
}

// CreateGatewaysInParallel creates multiple API Gateways concurrently
func (pm *ParallelManager) runGatewayOpsInParallel(
	gateways []string,
	opFunc func(string) GatewayResult,
) []GatewayResult {
	results := make([]GatewayResult, 0, len(gateways))
	resultChan := make(chan GatewayResult, len(gateways))

	sem := make(chan struct{}, pm.concurrency)

	var wg sync.WaitGroup
	for _, gateway := range gateways {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resultChan <- opFunc(name)
		}(gateway)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}

	return results
}

func (pm *ParallelManager) CreateGatewaysInParallel(
	ctx context.Context,
	gateways []string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
	models []utils.ModelDefinition,
) []GatewayResult {
	return pm.runGatewayOpsInParallel(gateways, func(name string) GatewayResult {
		return pm.createSingleGateway(ctx, name, routes, lambdaARNs, config, models)
	})
}

// createSingleGateway creates a single API Gateway with routes.
// Uses parallel route processing for improved performance.
// Requirements: 1.1, 1.2
func (pm *ParallelManager) createSingleGateway(
	ctx context.Context,
	gatewayName string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
	models []utils.ModelDefinition,
) GatewayResult {
	apiName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, gatewayName)

	utils.Info(ctx, "Creating API Gateway", map[string]interface{}{
		"gateway":  gatewayName,
		"api_name": apiName,
	})

	// Filter routes for this gateway
	gatewayRoutes := filterRoutesByGateway(routes, gatewayName)

	// First, check if an API Gateway with this name already exists (idempotency)
	apiID, err := pm.findExistingApiGateway(ctx, apiName)
	if err != nil {
		utils.Warn(ctx, "Error checking for existing API Gateway, will create new one", map[string]interface{}{
			"gateway": gatewayName,
			"error":   err.Error(),
		})
	}

	if apiID != "" {
		utils.Info(ctx, "Found existing API Gateway, adopting it", map[string]interface{}{
			"gateway":  gatewayName,
			"api_name": apiName,
			"api_id":   apiID,
		})
	}

	return pm.createGatewayViaOpenAPI(ctx, gatewayName, apiName, apiID, gatewayRoutes, routes, lambdaARNs, config, models)
}

// addLambdaPermissionsForGateway adds permissions for API Gateway to invoke Lambda functions.
// This creates aws_lambda_permission resources that allow the API Gateway to call the Lambda.
// Without these permissions, API Gateway returns 500 Internal Server Error.
func (pm *ParallelManager) addLambdaPermissionsForGateway(
	ctx context.Context,
	apiID string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
) error {
	// Track which Lambda functions we've already added permissions for
	// (one permission per Lambda, not per route)
	addedPermissions := make(map[string]bool)

	// Track which Lambdas need permissions (for logging)
	lambdasNeeded := make(map[string]bool)
	for _, route := range routes {
		if route.Lambda != "" {
			lambdasNeeded[route.Lambda] = true
		}
	}

	utils.Info(ctx, "Adding Lambda invoke permissions for API Gateway", map[string]interface{}{
		"api_id":         apiID,
		"route_count":    len(routes),
		"lambdas_needed": len(lambdasNeeded),
		"lambdas_available": len(lambdaARNs),
	})

	// Track errors and missing lambdas for better diagnostics
	var permissionErrors []string
	var missingLambdas []string

	for _, route := range routes {
		lambdaArn, exists := lambdaARNs[route.Lambda]
		if !exists {
			// Log warning for missing Lambda ARN - this is a potential bug indicator
			if route.Lambda != "" && !contains(missingLambdas, route.Lambda) {
				missingLambdas = append(missingLambdas, route.Lambda)
				utils.Warn(ctx, "Lambda ARN not found in map - cannot add API Gateway permission", map[string]interface{}{
					"lambda":     route.Lambda,
					"route_path": route.Path,
					"route_verb": route.Verb,
					"api_id":     apiID,
					"available_lambdas": getMapKeys(lambdaARNs),
				})
			}
			continue
		}

		// Skip if we've already added permission for this Lambda
		if addedPermissions[lambdaArn] {
			continue
		}

		// Extract function name from ARN
		functionName := extractLambdaNameFromArn(lambdaArn)

		// Create a unique statement ID for this permission
		// Use a stable ID based on API Gateway ID and function name
		statementId := fmt.Sprintf("AllowAPIGatewayInvoke-%s", strings.ReplaceAll(functionName, "-", ""))

		// Source ARN pattern allows all methods/paths/stages from this API Gateway
		sourceArn := fmt.Sprintf("arn:aws:execute-api:%s:%s:%s/*/*/*",
			config.AwsRegion, config.AwsAccountId, apiID)

		utils.Info(ctx, "Adding Lambda permission", map[string]interface{}{
			"function":     functionName,
			"statement_id": statementId,
			"source_arn":   sourceArn,
		})

		_, err := pm.clients.Lambda.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(statementId),
			Action:       aws.String("lambda:InvokeFunction"),
			Principal:    aws.String("apigateway.amazonaws.com"),
			SourceArn:    aws.String(sourceArn),
		})

		if err != nil {
			// Ignore if permission already exists (ResourceConflictException)
			if strings.Contains(err.Error(), "ResourceConflictException") {
				utils.Info(ctx, "Lambda permission already exists", map[string]interface{}{
					"function": functionName,
				})
			} else {
				errMsg := fmt.Sprintf("%s: %s", functionName, err.Error())
				permissionErrors = append(permissionErrors, errMsg)
				utils.Error(ctx, "Failed to add Lambda permission", map[string]interface{}{
					"function":     functionName,
					"statement_id": statementId,
					"source_arn":   sourceArn,
					"error":        err.Error(),
				})
			}
		} else {
			utils.Info(ctx, "Successfully added Lambda permission", map[string]interface{}{
				"function": functionName,
			})
		}

		addedPermissions[lambdaArn] = true
	}

	// Log summary of any issues
	if len(missingLambdas) > 0 {
		utils.Error(ctx, "Some Lambda ARNs were not found - API Gateway may return 500 errors", map[string]interface{}{
			"missing_lambdas": missingLambdas,
			"api_id":          apiID,
		})
	}

	// Return error if any permissions failed to be added
	if len(permissionErrors) > 0 {
		return fmt.Errorf("failed to add %d Lambda permission(s): %v", len(permissionErrors), permissionErrors)
	}

	return nil
}

// contains checks if a string slice contains a value
func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

// getMapKeys returns the keys of a string map for logging
func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// UpdateGatewaysInParallel updates multiple API Gateways concurrently
func (pm *ParallelManager) UpdateGatewaysInParallel(
	ctx context.Context,
	gateways []string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
	models []utils.ModelDefinition,
) []GatewayResult {
	return pm.runGatewayOpsInParallel(gateways, func(name string) GatewayResult {
		return pm.updateSingleGateway(ctx, name, routes, lambdaARNs, config, models)
	})
}

// updateSingleGateway updates a single API Gateway.
// Uses parallel route processing for improved performance.
// Requirements: 1.1, 1.2
func (pm *ParallelManager) updateSingleGateway(
	ctx context.Context,
	gatewayName string,
	routes []utils.Route,
	lambdaARNs map[string]string,
	config *DispatcherConfig,
	models []utils.ModelDefinition,
) GatewayResult {
	result := GatewayResult{Gateway: gatewayName}

	apiName := fmt.Sprintf("%s-%s-%s", config.AppName, config.Environment, gatewayName)

	utils.Info(ctx, "Updating API Gateway", map[string]interface{}{
		"gateway":  gatewayName,
		"api_name": apiName,
	})

	// Find existing API by name (uses Limit: 500 to handle accounts with >25 gateways)
	apiID, err := pm.findExistingApiGateway(ctx, apiName)
	if err != nil {
		result.Error = err
		return result
	}

	if apiID == "" {
		result.Error = fmt.Errorf("API Gateway not found: %s", apiName)
		return result
	}

	// Filter routes for this gateway
	gatewayRoutes := filterRoutesByGateway(routes, gatewayName)

	return pm.updateGatewayViaOpenAPI(ctx, gatewayName, apiID, gatewayRoutes, routes, lambdaARNs, config, models)
}

// DeleteGatewaysInParallel deletes multiple API Gateways concurrently
func (pm *ParallelManager) DeleteGatewaysInParallel(
	ctx context.Context,
	gateways []string,
	cloudWatchManager ...*CloudWatchManager,
) []GatewayResult {
	results := make([]GatewayResult, 0, len(gateways))
	resultChan := make(chan GatewayResult, len(gateways))

	sem := make(chan struct{}, pm.concurrency)

	// Get CloudWatchManager if provided
	var cwManager *CloudWatchManager
	if len(cloudWatchManager) > 0 {
		cwManager = cloudWatchManager[0]
	}

	var wg sync.WaitGroup
	for _, gateway := range gateways {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			result := pm.deleteSingleGateway(ctx, name, cwManager)
			resultChan <- result
		}(gateway)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}

	return results
}

// deleteSingleGateway deletes a single API Gateway and associated resources
func (pm *ParallelManager) deleteSingleGateway(
	ctx context.Context,
	gatewayName string,
	cloudWatchManager *CloudWatchManager,
) GatewayResult {
	result := GatewayResult{Gateway: gatewayName}

	apiName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, gatewayName)

	utils.Info(ctx, "Deleting API Gateway", map[string]interface{}{
		"gateway":  gatewayName,
		"api_name": apiName,
	})

	// Find existing API by name (uses Limit: 500 to handle accounts with >25 gateways)
	apiID, err := pm.findExistingApiGateway(ctx, apiName)
	if err != nil {
		result.Error = err
		return result
	}

	if apiID == "" {
		// API doesn't exist, consider it deleted
		utils.Info(ctx, "API Gateway not found, skipping deletion", map[string]interface{}{
			"gateway":  gatewayName,
			"api_name": apiName,
		})
		result.Success = true
		return result
	}

	// Delete the REST API (this also deletes deployments and stages)
	_, err = pm.clients.ApiGateway.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{
		RestApiId: aws.String(apiID),
	})
	if err != nil && !strings.Contains(err.Error(), "NotFoundException") {
		result.Error = fmt.Errorf("failed to delete REST API: %w", err)
		return result
	}

	// Delete CloudWatch log group for API Gateway if CloudWatchManager is provided
	if cloudWatchManager != nil {
		if err := cloudWatchManager.DeleteApiGatewayLogGroup(ctx, apiName); err != nil {
			utils.Warn(ctx, "Failed to delete API Gateway CloudWatch log group", map[string]interface{}{
				"gateway":  gatewayName,
				"api_name": apiName,
				"error":    err.Error(),
			})
			// Continue - log group deletion failure shouldn't fail the overall deletion
		}
	}

	result.Success = true

	utils.Info(ctx, "Successfully deleted API Gateway", map[string]interface{}{
		"gateway": gatewayName,
		"api_id":  apiID,
	})

	return result
}


// Helper functions

// getRouteProcessingConcurrency returns the concurrency limit for route processing.
// It uses RouteProcessingConcurrency if set, otherwise falls back to DockerBuildConcurrency,
// and finally defaults to a conservative value to avoid API Gateway rate limits.
// Requirements: 1.3, 3.4
func getRouteProcessingConcurrency(config *DispatcherConfig) int {
	if config == nil {
		return 5
	}

	// Use RouteProcessingConcurrency if explicitly set
	if config.RouteProcessingConcurrency > 0 {
		return config.RouteProcessingConcurrency
	}

	// Fall back to DockerBuildConcurrency, capped at 10
	if config.DockerBuildConcurrency > 0 {
		if config.DockerBuildConcurrency > 10 {
			return 10
		}
		return config.DockerBuildConcurrency
	}

	return 5
}

// filterRoutesByLambda returns routes that use the specified lambda
func filterRoutesByLambda(routes []utils.Route, lambdaName string) []utils.Route {
	filtered := make([]utils.Route, 0)
	for _, route := range routes {
		if route.Lambda == lambdaName {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

// filterRoutesByGateway returns routes that use the specified gateway
func filterRoutesByGateway(routes []utils.Route, gatewayName string) []utils.Route {
	filtered := make([]utils.Route, 0)
	for _, route := range routes {
		if route.Gateway == gatewayName {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

// buildEnvVars builds environment variables for a Lambda function
func (pm *ParallelManager) buildEnvVars(lambdaName string, routes []utils.Route, lambdaConfig map[string]interface{}) map[string]string {
	envVars := map[string]string{
		"APP_NAME":    pm.config.AppName,
		"ENVIRONMENT": pm.config.Environment,
		"ACTION":      lambdaName,
	}

	// Add CORS origin
	if len(pm.config.FrontendUrls) > 0 {
		envVars["FRONTEND_URL"] = pm.config.FrontendUrls[0]
		envVars["CORS_ALLOWED_ORIGINS"] = strings.Join(pm.config.FrontendUrls, ",")
	}

	// Collect tables from routes for this lambda
	tablesSet := make(map[string]bool)
	for _, route := range routes {
		if route.Lambda == lambdaName {
			for _, table := range route.Tables {
				tablesSet[table] = true
			}
		}
	}

	// Add read-only tables
	for _, table := range pm.config.ReadOnlyTables {
		tablesSet[table] = true
	}

	// Add read-write tables
	for _, table := range pm.config.ReadWriteTables {
		tablesSet[table] = true
	}

	// Build table environment variables (unless suppressed)
	if !pm.config.SuppressTableEnvVars {
		tables := make([]string, 0, len(tablesSet))
		for table := range tablesSet {
			tables = append(tables, table)
			normalizedTable := strings.ReplaceAll(table, "_", "-")
			fullTableName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, normalizedTable)
			envVarName := strings.ToUpper(table) + "_TABLE_NAME"
			envVars[envVarName] = fullTableName
		}

		if len(tables) > 0 {
			fullTableNames := make([]string, len(tables))
			for i, table := range tables {
				normalizedTable := strings.ReplaceAll(table, "_", "-")
				fullTableNames[i] = fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, normalizedTable)
			}
			envVars["TABLES"] = strings.Join(fullTableNames, ",")
		}
	}

	// Merge shared env vars from lambda_config
	if sharedConfigRaw, exists := lambdaConfig["shared"]; exists {
		if sharedConfig, ok := extractMapValue(sharedConfigRaw); ok {
			if envVarsRaw, exists := sharedConfig["env_vars"]; exists {
				if sharedEnvVars, ok := extractMapValue(envVarsRaw); ok {
					for k, v := range sharedEnvVars {
						if strVal, ok := extractStringValue(v); ok {
							envVars[k] = strVal
						}
					}
				}
			}
		}
	}

	// Merge lambda-specific env vars from lambda_config
	if lambdaConfigRaw, exists := lambdaConfig[lambdaName]; exists {
		if lambdaCfg, ok := extractMapValue(lambdaConfigRaw); ok {
			if envVarsRaw, exists := lambdaCfg["env_vars"]; exists {
				if lambdaEnvVars, ok := extractMapValue(envVarsRaw); ok {
					for k, v := range lambdaEnvVars {
						if strVal, ok := extractStringValue(v); ok {
							envVars[k] = strVal
						}
					}
				}
			}
		}
	}

	return envVars
}

// getTimeoutAndMemory gets timeout and memory settings for a Lambda
func (pm *ParallelManager) getTimeoutAndMemory(lambdaName string, lambdaConfig map[string]interface{}) (timeout, memory int64) {
	// Start with provider defaults
	timeout = pm.config.DefaultLambdaTimeout
	memory = pm.config.DefaultLambdaMemory

	if timeout <= 0 {
		timeout = 30
	}
	if memory <= 0 {
		memory = 128
	}

	// Check shared config
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

	// Check lambda-specific config (overrides shared)
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

	return timeout, memory
}

// setupTriggers sets up SNS/SQS triggers for a Lambda
func (pm *ParallelManager) setupTriggers(ctx context.Context, lambdaName, lambdaArn string, lambdaConfig map[string]interface{}) {
	lambdaConfigRaw, exists := lambdaConfig[lambdaName]
	if !exists {
		return
	}

	lambdaCfg, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return
	}

	// Handle SNS triggers
	if snsTriggersRaw, exists := lambdaCfg["sns_triggers"]; exists {
		pm.setupSNSTriggers(ctx, lambdaName, lambdaArn, snsTriggersRaw)
	}

	// Handle SQS triggers
	if sqsTriggersRaw, exists := lambdaCfg["sqs_triggers"]; exists {
		pm.setupSQSTriggers(ctx, lambdaName, lambdaArn, sqsTriggersRaw)
	}
}

// setupSNSTriggers sets up SNS triggers for a Lambda
func (pm *ParallelManager) setupSNSTriggers(ctx context.Context, lambdaName, lambdaArn string, triggersRaw interface{}) {
	triggers := convertToStableList(triggersRaw)
	for _, trigger := range triggers {
		triggerMap, ok := extractMapValue(trigger)
		if !ok {
			continue
		}

		topicArn, ok := triggerMap["topic_arn"].(string)
		if !ok {
			continue
		}

		utils.Info(ctx, "Setting up SNS trigger", map[string]interface{}{
			"lambda":    lambdaName,
			"topic_arn": topicArn,
		})

		// Add Lambda permission for SNS
		functionName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, lambdaName)
		statementID := fmt.Sprintf("sns-%s", strings.ReplaceAll(topicArn, ":", "-"))

		_, err := pm.clients.Lambda.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(statementID),
			Action:       aws.String("lambda:InvokeFunction"),
			Principal:    aws.String("sns.amazonaws.com"),
			SourceArn:    aws.String(topicArn),
		})
		if err != nil && !strings.Contains(err.Error(), "ResourceConflictException") {
			utils.Warn(ctx, "Failed to add SNS permission", map[string]interface{}{
				"lambda":    lambdaName,
				"topic_arn": topicArn,
				"error":     err.Error(),
			})
		}

		// Subscribe Lambda to SNS topic
		_, err = pm.clients.SNS.Subscribe(ctx, &sns.SubscribeInput{
			TopicArn: aws.String(topicArn),
			Protocol: aws.String("lambda"),
			Endpoint: aws.String(lambdaArn),
		})
		if err != nil {
			utils.Warn(ctx, "Failed to subscribe to SNS topic", map[string]interface{}{
				"lambda":    lambdaName,
				"topic_arn": topicArn,
				"error":     err.Error(),
			})
		}
	}
}

// setupSQSTriggers sets up SQS triggers for a Lambda
func (pm *ParallelManager) setupSQSTriggers(ctx context.Context, lambdaName, lambdaArn string, triggersRaw interface{}) {
	triggers := convertToStableList(triggersRaw)
	for _, trigger := range triggers {
		triggerMap, ok := extractMapValue(trigger)
		if !ok {
			continue
		}

		queueArn, ok := triggerMap["queue_arn"].(string)
		if !ok {
			continue
		}

		utils.Info(ctx, "Setting up SQS trigger", map[string]interface{}{
			"lambda":    lambdaName,
			"queue_arn": queueArn,
		})

		// Create event source mapping
		batchSize := int32(10)
		if bs, exists := triggerMap["batch_size"]; exists {
			if bsVal, ok := extractInt64Value(bs); ok {
				batchSize = int32(bsVal)
			}
		}

		_, err := pm.clients.Lambda.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
			FunctionName:   aws.String(lambdaArn),
			EventSourceArn: aws.String(queueArn),
			BatchSize:      aws.Int32(batchSize),
			Enabled:        aws.Bool(true),
		})
		if err != nil && !strings.Contains(err.Error(), "ResourceConflictException") {
			utils.Warn(ctx, "Failed to create SQS event source mapping", map[string]interface{}{
				"lambda":    lambdaName,
				"queue_arn": queueArn,
				"error":     err.Error(),
			})
		}
	}
}

// extractInt64Value extracts an int64 from various types
func extractInt64Value(v interface{}) (int64, bool) {
	switch val := v.(type) {
	case types.Dynamic:
		// DynamicAttribute wraps values in types.Dynamic — unwrap and recurse
		if !val.IsNull() && !val.IsUnknown() {
			return extractInt64Value(val.UnderlyingValue())
		}
		return 0, false
	case int64:
		return val, true
	case int:
		return int64(val), true
	case float64:
		return int64(val), true
	case int32:
		return int64(val), true
	case types.Number:
		if !val.IsNull() && !val.IsUnknown() {
			bf := val.ValueBigFloat()
			i, _ := bf.Int64()
			return i, true
		}
		return 0, false
	case types.Int64:
		if !val.IsNull() && !val.IsUnknown() {
			return val.ValueInt64(), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// snsSubscribeInput is a placeholder for SNS Subscribe input
type snsSubscribeInput struct {
	TopicArn *string
	Protocol *string
	Endpoint *string
}

// LambdaStateResult represents the result of reading Lambda state from AWS
type LambdaStateResult struct {
	Action       string
	ARN          string
	FunctionName string
	Exists       bool
	Runtime      string
	Handler      string
	MemorySize   int32
	Timeout      int32
	LastModified string
	EnvVars      map[string]string // Actual env vars from AWS for drift detection
	Error        error
}

// GatewayStateResult represents the result of reading Gateway state from AWS
type GatewayStateResult struct {
	Gateway    string
	ID         string
	URL        string
	Exists     bool
	Name       string
	RouteCount int
	Error      error
}

// ReadLambdasInParallel reads the state of multiple Lambda functions concurrently
func (pm *ParallelManager) ReadLambdasInParallel(
	ctx context.Context,
	lambdas []string,
) []LambdaStateResult {
	results := make([]LambdaStateResult, 0, len(lambdas))
	resultChan := make(chan LambdaStateResult, len(lambdas))

	sem := make(chan struct{}, pm.concurrency)

	var wg sync.WaitGroup
	for _, lambdaName := range lambdas {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resultChan <- pm.readSingleLambda(ctx, name)
		}(lambdaName)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}
	return results
}

// readSingleLambda reads the state of a single Lambda function from AWS
func (pm *ParallelManager) readSingleLambda(
	ctx context.Context,
	lambdaName string,
) LambdaStateResult {
	result := LambdaStateResult{Action: lambdaName}

	functionName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, lambdaName)
	result.FunctionName = functionName

	utils.Debug(ctx, "Reading Lambda function state", map[string]interface{}{
		"lambda":        lambdaName,
		"function_name": functionName,
	})

	// Get Lambda function details
	getOutput, err := pm.clients.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String(functionName),
	})
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			// Lambda doesn't exist
			result.Exists = false
			return result
		}
		result.Error = fmt.Errorf("failed to get Lambda function: %w", err)
		return result
	}

	// Populate result with Lambda details
	result.Exists = true
	if getOutput.Configuration != nil {
		if getOutput.Configuration.FunctionArn != nil {
			result.ARN = *getOutput.Configuration.FunctionArn
		}
		result.Runtime = string(getOutput.Configuration.Runtime)
		if getOutput.Configuration.Handler != nil {
			result.Handler = *getOutput.Configuration.Handler
		}
		if getOutput.Configuration.MemorySize != nil {
			result.MemorySize = *getOutput.Configuration.MemorySize
		}
		if getOutput.Configuration.Timeout != nil {
			result.Timeout = *getOutput.Configuration.Timeout
		}
		if getOutput.Configuration.LastModified != nil {
			result.LastModified = *getOutput.Configuration.LastModified
		}
		if getOutput.Configuration.Environment != nil && getOutput.Configuration.Environment.Variables != nil {
			result.EnvVars = getOutput.Configuration.Environment.Variables
		}
	}

	utils.Debug(ctx, "Read Lambda function state", map[string]interface{}{
		"lambda": lambdaName,
		"exists": result.Exists,
		"arn":    result.ARN,
	})

	return result
}

// ReadGatewaysInParallel reads the state of multiple API Gateways concurrently
func (pm *ParallelManager) ReadGatewaysInParallel(
	ctx context.Context,
	gateways []string,
) []GatewayStateResult {
	results := make([]GatewayStateResult, 0, len(gateways))
	resultChan := make(chan GatewayStateResult, len(gateways))

	sem := make(chan struct{}, pm.concurrency)

	var wg sync.WaitGroup
	for _, gateway := range gateways {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			result := pm.readSingleGateway(ctx, name)
			resultChan <- result
		}(gateway)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}

	return results
}

// readSingleGateway reads the state of a single API Gateway from AWS
func (pm *ParallelManager) readSingleGateway(
	ctx context.Context,
	gatewayName string,
) GatewayStateResult {
	result := GatewayStateResult{Gateway: gatewayName}

	apiName := fmt.Sprintf("%s-%s-%s", pm.config.AppName, pm.config.Environment, gatewayName)

	utils.Debug(ctx, "Reading API Gateway state", map[string]interface{}{
		"gateway":  gatewayName,
		"api_name": apiName,
	})

	// Find existing API by name (uses Limit: 500 to handle accounts with >25 gateways)
	apiID, err := pm.findExistingApiGateway(ctx, apiName)
	if err != nil {
		result.Error = err
		return result
	}

	if apiID == "" {
		// API doesn't exist
		result.Exists = false
		return result
	}

	result.Exists = true
	result.ID = apiID
	result.Name = apiName
	result.URL = fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com/%s", apiID, pm.config.AwsRegion, pm.config.Environment)

	// Get resources to count routes
	resourcesOutput, err := pm.clients.ApiGateway.GetResources(ctx, &apigateway.GetResourcesInput{
		RestApiId: aws.String(apiID),
	})
	if err != nil {
		utils.Warn(ctx, "Failed to get API Gateway resources", map[string]interface{}{
			"gateway": gatewayName,
			"error":   err.Error(),
		})
	} else {
		// Count methods across all resources (excluding root)
		routeCount := 0
		for _, resource := range resourcesOutput.Items {
			if resource.ResourceMethods != nil {
				routeCount += len(resource.ResourceMethods)
			}
		}
		result.RouteCount = routeCount
	}

	utils.Debug(ctx, "Read API Gateway state", map[string]interface{}{
		"gateway":     gatewayName,
		"exists":      result.Exists,
		"api_id":      result.ID,
		"route_count": result.RouteCount,
	})

	return result
}
