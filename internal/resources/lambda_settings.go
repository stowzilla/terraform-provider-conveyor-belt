package resources

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// ThrottleSettings is an API Gateway token-bucket limit: steady-state requests
// per second plus the burst the bucket can absorb.
type ThrottleSettings struct {
	Rate  float64 `json:"rate"`
	Burst int64   `json:"burst"`
}

// LambdaCapacity carries the per-function settings from config/lambda/<fn>.yml
// (or the HCL lambda_config map) that are applied outside
// Create/UpdateFunctionConfiguration's timeout/memory pair.
type LambdaCapacity struct {
	// ReservedConcurrency is nil when the key is absent (no reservation; AWS
	// default). 0 is a valid value and disables the function.
	ReservedConcurrency *int64 `json:",omitempty"`
	// EphemeralStorage is /tmp in MB (512–10240). 0 means "not set" and the
	// function is updated to the AWS default of 512.
	EphemeralStorage int64 `json:",omitempty"`
}

// defaultEphemeralStorageMB is the Lambda default for /tmp.
const defaultEphemeralStorageMB int64 = 512

// lambdaSettingLayers returns the "shared" map followed by the lambda's own
// map so callers can apply the usual shared-then-override precedence.
func lambdaSettingLayers(lambdaConfig map[string]interface{}, lambdaName string) []map[string]interface{} {
	var layers []map[string]interface{}
	if sharedRaw, exists := lambdaConfig["shared"]; exists {
		if shared, ok := extractMapValue(sharedRaw); ok {
			layers = append(layers, shared)
		}
	}
	if ownRaw, exists := lambdaConfig[lambdaName]; exists {
		if own, ok := extractMapValue(ownRaw); ok {
			layers = append(layers, own)
		}
	}
	return layers
}

// extractLambdaCapacity reads reserved_concurrency and ephemeral_storage for a
// lambda, with lambda-specific values overriding "shared".
func extractLambdaCapacity(lambdaConfig map[string]interface{}, lambdaName string) LambdaCapacity {
	var cap LambdaCapacity
	for _, layer := range lambdaSettingLayers(lambdaConfig, lambdaName) {
		if v, exists := layer["reserved_concurrency"]; exists {
			if n, ok := extractInt64Value(v); ok {
				n := n
				cap.ReservedConcurrency = &n
			}
		}
		if v, exists := layer["ephemeral_storage"]; exists {
			if n, ok := extractInt64Value(v); ok {
				cap.EphemeralStorage = n
			}
		}
	}
	return cap
}

// extractLambdaThrottle reads a lambda's `throttle: {rate, burst}` block, with
// lambda-specific values overriding "shared". Returns nil when unset.
func extractLambdaThrottle(lambdaConfig map[string]interface{}, lambdaName string) *ThrottleSettings {
	var result *ThrottleSettings
	for _, layer := range lambdaSettingLayers(lambdaConfig, lambdaName) {
		raw, exists := layer["throttle"]
		if !exists {
			continue
		}
		if t := parseThrottleSettings(raw); t != nil {
			result = t
		}
	}
	return result
}

// parseThrottleSettings converts a {rate, burst} map (YAML or HCL) into
// ThrottleSettings. Returns nil when the value is not a map or has neither key.
func parseThrottleSettings(raw interface{}) *ThrottleSettings {
	m, ok := extractMapValue(raw)
	if !ok {
		return nil
	}
	t := &ThrottleSettings{}
	found := false
	if v, exists := m["rate"]; exists {
		if f, ok := extractFloat64Value(v); ok {
			t.Rate = f
			found = true
		}
	}
	if v, exists := m["burst"]; exists {
		if n, ok := extractInt64Value(v); ok {
			t.Burst = n
			found = true
		}
	}
	if !found {
		return nil
	}
	return t
}

// extractFloat64Value mirrors extractInt64Value for fractional rate limits
// (e.g. `rate: 0.5`). YAML ints, floats, and Terraform number types are accepted.
func extractFloat64Value(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case types.Dynamic:
		if !val.IsNull() && !val.IsUnknown() {
			return extractFloat64Value(val.UnderlyingValue())
		}
		return 0, false
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	case types.Number:
		if !val.IsNull() && !val.IsUnknown() {
			f, _ := val.ValueBigFloat().Float64()
			return f, true
		}
		return 0, false
	case types.Float64:
		if !val.IsNull() && !val.IsUnknown() {
			return val.ValueFloat64(), true
		}
		return 0, false
	case types.Int64:
		if !val.IsNull() && !val.IsUnknown() {
			return float64(val.ValueInt64()), true
		}
		return 0, false
	}
	if n, ok := extractInt64Value(v); ok {
		return float64(n), true
	}
	return 0, false
}

// validateLambdaSettings rejects malformed reserved_concurrency,
// ephemeral_storage, and throttle values at plan time so a typo in
// config/lambda/<fn>.yml fails the plan instead of being dropped silently.
func validateLambdaSettings(lambdaConfig map[string]interface{}) []error {
	var errs []error
	names := make([]string, 0, len(lambdaConfig))
	for name := range lambdaConfig {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg, ok := extractMapValue(lambdaConfig[name])
		if !ok {
			continue
		}
		if v, exists := cfg["reserved_concurrency"]; exists {
			n, ok := extractInt64Value(v)
			if !ok || n < 0 {
				errs = append(errs, fmt.Errorf("%s: reserved_concurrency must be an integer >= 0 (got %v)", name, v))
			}
		}
		if v, exists := cfg["ephemeral_storage"]; exists {
			n, ok := extractInt64Value(v)
			if !ok || n < 512 || n > 10240 {
				errs = append(errs, fmt.Errorf("%s: ephemeral_storage must be an integer between 512 and 10240 MB (got %v)", name, v))
			}
		}
		if v, exists := cfg["throttle"]; exists {
			t := parseThrottleSettings(v)
			if t == nil || t.Rate <= 0 || t.Burst <= 0 {
				errs = append(errs, fmt.Errorf("%s: throttle must be a map with rate > 0 and burst > 0 (got %v)", name, v))
			}
		}
	}
	return errs
}

// methodSettingKey returns the API Gateway stage method-settings key for a
// route: the resource path with "/" escaped as "~1", then "/" + HTTP method.
// "/connect/chat" + "POST" → "~1connect~1chat/POST".
func methodSettingKey(routePath, verb string) string {
	p := routePath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.ReplaceAll(p, "/", "~1") + "/" + strings.ToUpper(verb)
}

// effectiveRouteThrottles resolves the throttle for every route on a gateway:
// a route-level `throttle:` from routes.rb wins over the serving lambda's
// `throttle:` from config/lambda/<fn>.yml. Keys are method-settings keys.
func effectiveRouteThrottles(gatewayRoutes []utils.Route, lambdaConfig map[string]interface{}) map[string]ThrottleSettings {
	result := make(map[string]ThrottleSettings)
	lambdaThrottles := make(map[string]*ThrottleSettings)
	for _, route := range gatewayRoutes {
		var t *ThrottleSettings
		if route.Throttle != nil {
			t = &ThrottleSettings{Rate: route.Throttle.Rate, Burst: route.Throttle.Burst}
		} else {
			lt, seen := lambdaThrottles[route.Lambda]
			if !seen {
				lt = extractLambdaThrottle(lambdaConfig, route.Lambda)
				lambdaThrottles[route.Lambda] = lt
			}
			t = lt
		}
		if t == nil || (t.Rate <= 0 && t.Burst <= 0) {
			continue
		}
		result[methodSettingKey(route.Path, route.Verb)] = *t
	}
	return result
}

// gatewayHashInputs is everything besides the routes themselves that must
// change a gateway's hash (and therefore trigger a PutRestApi + stage update).
type gatewayHashInputs struct {
	FrontendUrls  []string
	LambdaConfig  map[string]interface{}
	StageThrottle *ThrottleSettings
	WafWebAclArn  string
}

// gatewayHashInputsFromConfig builds the hash inputs from a DispatcherConfig.
func gatewayHashInputsFromConfig(config *DispatcherConfig) gatewayHashInputs {
	if config == nil {
		return gatewayHashInputs{}
	}
	return gatewayHashInputs{
		FrontendUrls:  config.FrontendUrls,
		LambdaConfig:  config.LambdaConfig,
		StageThrottle: config.StageThrottle,
		WafWebAclArn:  config.WafWebAclArn,
	}
}
