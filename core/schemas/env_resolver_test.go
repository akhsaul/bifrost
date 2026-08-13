package schemas

import (
	"os"
	"strings"
	"testing"
)

func TestResolveEnvString(t *testing.T) {
	os.Setenv("TEST_KEY", "test_value")
	os.Setenv("HOST", "api.example.com")
	defer os.Unsetenv("TEST_KEY")
	defer os.Unsetenv("HOST")

	tests := []struct {
		name      string
		input     string
		want      string
		wantError bool
	}{
		{"exact match", "env.TEST_KEY", "test_value", false},
		{"exact match missing", "env.MISSING_KEY", "", true},
		{"interpolation", "https://${env.HOST}/v1", "https://api.example.com/v1", false},
		{"interpolation missing", "https://${env.MISSING_HOST}/v1", "", true},
		{"invalid exact reference", "env.INVALID-NAME", "", true},
		{"invalid interpolation", "https://${env.INVALID-NAME}/v1", "", true},
		{"plain text", "https://api.example.com", "https://api.example.com", false},
		{"empty string", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveEnvString(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("ResolveEnvString() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if got != tt.want {
				t.Errorf("ResolveEnvString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveNetworkConfig(t *testing.T) {
	t.Setenv("TEST_PROVIDER_URL", "https://provider.example/v1")
	t.Setenv("TEST_PROVIDER_TOKEN", "secret-token")

	config := NetworkConfig{
		BaseURL: "env.TEST_PROVIDER_URL",
		ExtraHeaders: map[string]string{
			"Authorization": "Bearer ${env.TEST_PROVIDER_TOKEN}",
			"X-Literal":     "literal",
		},
	}
	resolved, err := ResolveNetworkConfig(config)
	if err != nil {
		t.Fatalf("ResolveNetworkConfig() error = %v", err)
	}
	if resolved.BaseURL != "https://provider.example/v1" || resolved.ExtraHeaders["Authorization"] != "Bearer secret-token" {
		t.Fatalf("unexpected resolved config: %+v", resolved)
	}
	if config.BaseURL != "env.TEST_PROVIDER_URL" || config.ExtraHeaders["Authorization"] != "Bearer ${env.TEST_PROVIDER_TOKEN}" {
		t.Fatalf("input config was mutated: %+v", config)
	}
}

func TestMaskEnvString(t *testing.T) {
	t.Setenv("TEST_MASKED_VALUE", "api-key")
	got, err := MaskEnvString("bearer ${env.TEST_MASKED_VALUE}")
	if err != nil {
		t.Fatalf("MaskEnvString() error = %v", err)
	}
	if got != "bearer a*****y" {
		t.Fatalf("MaskEnvString() = %q", got)
	}
	if got == "bearer api-key" || strings.Contains(got, "api-key") {
		t.Fatalf("MaskEnvString leaked the resolved value: %q", got)
	}
	if EnvReferenceLabel("bearer ${env.TEST_MASKED_VALUE}") != "env.TEST_MASKED_VALUE" {
		t.Fatal("EnvReferenceLabel did not return the env key")
	}
}
