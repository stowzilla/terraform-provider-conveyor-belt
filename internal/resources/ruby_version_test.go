package resources

import (
	"errors"
	"reflect"
	"testing"

	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

func TestGetLambdaRuntime(t *testing.T) {
	tests := []struct {
		name        string
		rubyVersion string
		wantRuntime string
		wantImage   string
	}{
		{"default", "", "ruby3.4", "public.ecr.aws/sam/build-ruby3.4:latest-x86_64"},
		{"explicit 3.4", "3.4", "ruby3.4", "public.ecr.aws/sam/build-ruby3.4:latest-x86_64"},
		{"ruby 4.0", "4.0", "ruby4.0", "public.ecr.aws/sam/build-ruby4.0:latest-x86_64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &DispatcherConfig{RubyVersion: tt.rubyVersion}
			if got := c.GetLambdaRuntime(); got != tt.wantRuntime {
				t.Errorf("GetLambdaRuntime() = %q, want %q", got, tt.wantRuntime)
			}
			if got := c.GetDockerBuildImage(); got != tt.wantImage {
				t.Errorf("GetDockerBuildImage() = %q, want %q", got, tt.wantImage)
			}
		})
	}
}

func TestLambdaRuntimeDrifted(t *testing.T) {
	tests := []struct {
		actual, expected string
		want             bool
	}{
		{"ruby3.4", "ruby4.0", true},
		{"ruby4.0", "ruby3.4", true},
		{"ruby4.0", "ruby4.0", false},
		{"ruby3.4", "ruby3.4", false},
		{"", "ruby4.0", false}, // unknown actual runtime (e.g. image-based function)
		{"ruby3.4", "", false},
	}
	for _, tt := range tests {
		if got := lambdaRuntimeDrifted(tt.actual, tt.expected); got != tt.want {
			t.Errorf("lambdaRuntimeDrifted(%q, %q) = %v, want %v", tt.actual, tt.expected, got, tt.want)
		}
	}
}

func TestDetectRuntimeDrift(t *testing.T) {
	results := []LambdaStateResult{
		{Action: "users_show", Exists: true, Runtime: "ruby3.4"},
		{Action: "orders_create", Exists: true, Runtime: "ruby4.0"},
		{Action: "admin_index", Exists: true, Runtime: "ruby3.4"},
		{Action: "missing", Exists: false, Runtime: ""},
		{Action: "errored", Exists: true, Runtime: "ruby3.4", Error: errors.New("boom")},
		{Action: "image_based", Exists: true, Runtime: ""},
	}

	got := detectRuntimeDrift(results, "ruby4.0")
	want := []string{"admin_index", "users_show"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detectRuntimeDrift(ruby4.0) = %v, want %v", got, want)
	}

	// Nothing drifts when every function already matches.
	if got := detectRuntimeDrift(results[1:2], "ruby4.0"); len(got) != 0 {
		t.Errorf("expected no drift, got %v", got)
	}
}

func TestBuildUpdateFunctionConfigurationInputIncludesRuntime(t *testing.T) {
	envVars := map[string]string{"FOO": "bar"}

	for _, tc := range []struct {
		rubyVersion string
		want        lambdaTypes.Runtime
	}{
		{"", lambdaTypes.Runtime("ruby3.4")},
		{"4.0", lambdaTypes.Runtime("ruby4.0")},
	} {
		cfg := &DispatcherConfig{RubyVersion: tc.rubyVersion}
		input := buildUpdateFunctionConfigurationInput("app-dev-users_show", cfg, envVars, 30, 512, nil)
		if input.Runtime != tc.want {
			t.Errorf("ruby_version %q: Runtime = %q, want %q", tc.rubyVersion, input.Runtime, tc.want)
		}
		if *input.FunctionName != "app-dev-users_show" || *input.Timeout != 30 || *input.MemorySize != 512 {
			t.Errorf("unexpected input fields: %+v", input)
		}
		if input.Environment == nil || input.Environment.Variables["FOO"] != "bar" {
			t.Errorf("env vars not set: %+v", input.Environment)
		}
		if input.VpcConfig != nil || input.Layers != nil {
			t.Errorf("expected no VPC/layers, got %+v / %v", input.VpcConfig, input.Layers)
		}
	}

	cfg := &DispatcherConfig{RubyVersion: "4.0", LambdaLayerArns: []string{"arn:aws:lambda:us-east-1:123:layer:x:1"}}
	vpc := &VpcConfig{SubnetIds: []string{"subnet-1"}, SecurityGroupIds: []string{"sg-1"}}
	input := buildUpdateFunctionConfigurationInput("fn", cfg, nil, 10, 128, vpc)
	if len(input.Layers) != 1 || input.VpcConfig == nil || input.VpcConfig.SubnetIds[0] != "subnet-1" {
		t.Errorf("layers/VPC not propagated: %+v", input)
	}
}
