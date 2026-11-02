package resources

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigatewayTypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"

	"terraform-provider-conveyor-belt/internal/utils"
)

// stageArn returns the ARN API Gateway uses for a REST API stage, which is the
// resource ARN WAFv2 expects for web ACL associations.
func stageArn(region, apiID, stageName string) string {
	return fmt.Sprintf("arn:aws:apigateway:%s::/restapis/%s/stages/%s", region, apiID, stageName)
}

// reconcileStageSettings makes a gateway's stage match the configured
// throttling and WAF association. It runs after every deployment of a gateway
// (create and update) and is declarative: method-level throttle overrides that
// are no longer configured are removed, so the stage never keeps a stale limit.
//
// Keys are API Gateway method-settings keys: "*/*" for the stage default and
// "~1path~1segments/VERB" for a single route.
func (pm *ParallelManager) reconcileStageSettings(
	ctx context.Context,
	apiID string,
	gatewayName string,
	gatewayRoutes []utils.Route,
	config *DispatcherConfig,
) error {
	stageName := config.Environment

	desired := effectiveRouteThrottles(gatewayRoutes, config.LambdaConfig)
	if config.StageThrottle != nil && (config.StageThrottle.Rate > 0 || config.StageThrottle.Burst > 0) {
		desired["*/*"] = *config.StageThrottle
	}

	getOutput, err := pm.clients.ApiGateway.GetStage(ctx, &apigateway.GetStageInput{
		RestApiId: aws.String(apiID),
		StageName: aws.String(stageName),
	})
	if err != nil {
		return fmt.Errorf("failed to read stage %s for gateway '%s': %w", stageName, gatewayName, err)
	}

	var ops []apigatewayTypes.PatchOperation

	// Remove method settings that are no longer configured. Sorted for stable logs.
	var stale []string
	for key := range getOutput.MethodSettings {
		if _, keep := desired[key]; !keep {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		ops = append(ops, apigatewayTypes.PatchOperation{
			Op:   apigatewayTypes.OpRemove,
			Path: aws.String("/" + key),
		})
	}

	// Apply (or refresh) the configured limits.
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t := desired[key]
		ops = append(ops,
			apigatewayTypes.PatchOperation{
				Op:    apigatewayTypes.OpReplace,
				Path:  aws.String("/" + key + "/throttling/rateLimit"),
				Value: aws.String(strconv.FormatFloat(t.Rate, 'f', -1, 64)),
			},
			apigatewayTypes.PatchOperation{
				Op:    apigatewayTypes.OpReplace,
				Path:  aws.String("/" + key + "/throttling/burstLimit"),
				Value: aws.String(strconv.FormatInt(t.Burst, 10)),
			},
		)
	}

	if len(ops) > 0 {
		var updateErr error
		for attempt := 0; attempt < 5; attempt++ {
			_, updateErr = pm.clients.ApiGateway.UpdateStage(ctx, &apigateway.UpdateStageInput{
				RestApiId:       aws.String(apiID),
				StageName:       aws.String(stageName),
				PatchOperations: ops,
			})
			if updateErr == nil {
				break
			}
			if strings.Contains(updateErr.Error(), "TooManyRequests") || strings.Contains(updateErr.Error(), "429") {
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
				continue
			}
			break
		}
		if updateErr != nil {
			return fmt.Errorf("failed to update throttling on stage %s for gateway '%s': %w", stageName, gatewayName, updateErr)
		}
		utils.Info(ctx, "Reconciled stage throttling", map[string]interface{}{
			"gateway":       gatewayName,
			"stage":         stageName,
			"throttle_keys": keys,
			"removed_keys":  stale,
		})
	}

	return pm.reconcileWafAssociation(ctx, apiID, gatewayName, config)
}

// reconcileWafAssociation attaches waf_web_acl_arn to the stage, or detaches
// the ACL the provider previously attached when the attribute is removed. An
// ACL attached by something else (e.g. an aws_wafv2_web_acl_association in the
// app's own Terraform) is left alone.
func (pm *ParallelManager) reconcileWafAssociation(
	ctx context.Context,
	apiID string,
	gatewayName string,
	config *DispatcherConfig,
) error {
	if pm.clients.WAF == nil {
		return nil
	}
	if config.WafWebAclArn == "" && config.PreviousWafWebAclArn == "" {
		return nil
	}

	resourceArn := stageArn(config.AwsRegion, apiID, config.Environment)

	current := ""
	getOutput, err := pm.clients.WAF.GetWebACLForResource(ctx, &wafv2.GetWebACLForResourceInput{
		ResourceArn: aws.String(resourceArn),
	})
	if err != nil {
		return fmt.Errorf("failed to read WAF association for gateway '%s': %w", gatewayName, err)
	}
	if getOutput.WebACL != nil && getOutput.WebACL.ARN != nil {
		current = *getOutput.WebACL.ARN
	}

	if config.WafWebAclArn != "" {
		if current == config.WafWebAclArn {
			return nil
		}
		// A freshly created web ACL can take a few seconds to become associable.
		var assocErr error
		for attempt := 0; attempt < 6; attempt++ {
			_, assocErr = pm.clients.WAF.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
				ResourceArn: aws.String(resourceArn),
				WebACLArn:   aws.String(config.WafWebAclArn),
			})
			if assocErr == nil {
				break
			}
			if strings.Contains(assocErr.Error(), "WAFUnavailableEntityException") {
				time.Sleep(time.Duration(attempt+1) * 5 * time.Second)
				continue
			}
			break
		}
		if assocErr != nil {
			return fmt.Errorf("failed to associate WAF web ACL with gateway '%s' stage: %w", gatewayName, assocErr)
		}
		utils.Info(ctx, "Associated WAF web ACL with stage", map[string]interface{}{
			"gateway":     gatewayName,
			"web_acl_arn": config.WafWebAclArn,
		})
		return nil
	}

	// waf_web_acl_arn was removed: only detach if the stage still carries the
	// ACL we attached, never one that something else manages.
	if current != "" && current == config.PreviousWafWebAclArn {
		if _, err := pm.clients.WAF.DisassociateWebACL(ctx, &wafv2.DisassociateWebACLInput{
			ResourceArn: aws.String(resourceArn),
		}); err != nil {
			return fmt.Errorf("failed to disassociate WAF web ACL from gateway '%s' stage: %w", gatewayName, err)
		}
		utils.Info(ctx, "Disassociated WAF web ACL from stage", map[string]interface{}{
			"gateway":     gatewayName,
			"web_acl_arn": current,
		})
	}
	return nil
}
