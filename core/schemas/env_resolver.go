package schemas

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envInterpolationRegex matches ${env.KEY_NAME} patterns
var envInterpolationRegex = regexp.MustCompile(`\$\{env\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// validEnvKeyRegex strictly matches env.KEY_NAME
var validEnvKeyRegex = regexp.MustCompile(`^env\.([A-Za-z_][A-Za-z0-9_]*)$`)

// IsEnvReferenceString reports whether input contains an environment
// reference understood by ResolveEnvString.
func IsEnvReferenceString(input string) bool {
	return validEnvKeyRegex.MatchString(input) || envInterpolationRegex.MatchString(input)
}

// EnvReferenceLabel returns the first env.KEY reference represented by input.
// It is intended for UI labels, while input itself remains the full template.
func EnvReferenceLabel(input string) string {
	if validEnvKeyRegex.MatchString(input) {
		return input
	}
	if match := envInterpolationRegex.FindStringSubmatch(input); len(match) == 2 {
		return "env." + match[1]
	}
	return ""
}

// MaskEnvString resolves an environment-backed string and returns a masked
// representation suitable for API responses. Plain strings are returned
// unchanged. The resolved value is never returned by this function.
func MaskEnvString(input string) (string, error) {
	if !IsEnvReferenceString(input) {
		return input, nil
	}
	maskValue := func(value string) string {
		if len(value) <= 1 {
			return "*"
		}
		return value[:1] + strings.Repeat("*", len(value)-2) + value[len(value)-1:]
	}
	if validEnvKeyRegex.MatchString(input) {
		key := validEnvKeyRegex.FindStringSubmatch(input)[1]
		value, ok := os.LookupEnv(key)
		if !ok || value == "" {
			return "", fmt.Errorf("environment variable %q is empty or not set", key)
		}
		return maskValue(value), nil
	}
	return envInterpolationRegex.ReplaceAllStringFunc(input, func(match string) string {
		key := envInterpolationRegex.FindStringSubmatch(match)[1]
		value, ok := os.LookupEnv(key)
		if !ok || value == "" {
			return match
		}
		return maskValue(value)
	}), validateEnvInterpolation(input)
}

func validateEnvInterpolation(input string) error {
	var missing string
	envInterpolationRegex.ReplaceAllStringFunc(input, func(match string) string {
		key := envInterpolationRegex.FindStringSubmatch(match)[1]
		if value, ok := os.LookupEnv(key); !ok || value == "" {
			missing = key
		}
		return match
	})
	if missing != "" {
		return fmt.Errorf("environment variable %q is empty or not set", missing)
	}
	return nil
}

// ResolveEnvString resolves environment variables in the given input string.
// If the input is exactly "env.KEY", it returns the environment variable KEY.
// If the input contains "${env.KEY}", it replaces all such occurrences with their environment variable values.
// If an environment variable is missing or empty, it returns an error that DOES NOT leak secrets.
func ResolveEnvString(input string) (string, error) {
	if input == "" {
		return "", nil
	}

	// Case 1: Exact match "env.KEY"
	if validEnvKeyRegex.MatchString(input) {
		matches := validEnvKeyRegex.FindStringSubmatch(input)
		key := matches[1]
		val := os.Getenv(key)
		if val == "" {
			return "", fmt.Errorf("environment variable %q is empty or not set", key)
		}
		return val, nil
	}
	if strings.HasPrefix(input, "env.") {
		return "", fmt.Errorf("invalid environment variable reference")
	}

	// Case 2: Interpolation "${env.KEY}"
	if envInterpolationRegex.MatchString(input) {
		var missingKeys []string
		result := envInterpolationRegex.ReplaceAllStringFunc(input, func(match string) string {
			// Extract key from "${env.KEY}"
			matches := envInterpolationRegex.FindStringSubmatch(match)
			if len(matches) < 2 {
				return match
			}
			key := matches[1]
			val := os.Getenv(key)
			if val == "" {
				missingKeys = append(missingKeys, key)
				return match // Return original, though it will error out anyway
			}
			return val
		})

		if len(missingKeys) > 0 {
			// Deduplicate missing keys for cleaner error messages
			seen := make(map[string]bool)
			var uniqueMissing []string
			for _, k := range missingKeys {
				if !seen[k] {
					seen[k] = true
					uniqueMissing = append(uniqueMissing, k)
				}
			}
			return "", fmt.Errorf("missing or empty environment variables for interpolation: %s", strings.Join(uniqueMissing, ", "))
		}
		return result, nil
	}
	if strings.Contains(input, "${env.") {
		return "", fmt.Errorf("invalid environment variable interpolation")
	}

	// Case 3: Plain text, no env variables referenced
	return input, nil
}

// ResolveNetworkConfig returns a runtime copy of a network configuration with
// environment references resolved. The input is never mutated, so declarative
// config can safely retain env.* references for persistence and redacted API
// responses.
func ResolveNetworkConfig(config NetworkConfig) (NetworkConfig, error) {
	resolved := config

	var err error
	if resolved.BaseURL, err = ResolveEnvString(config.BaseURL); err != nil {
		return NetworkConfig{}, fmt.Errorf("base_url: %w", err)
	}
	if config.ExtraHeaders != nil {
		resolved.ExtraHeaders = make(map[string]string, len(config.ExtraHeaders))
		for key, value := range config.ExtraHeaders {
			resolvedValue, resolveErr := ResolveEnvString(value)
			if resolveErr != nil {
				return NetworkConfig{}, fmt.Errorf("extra_headers[%q]: %w", key, resolveErr)
			}
			resolved.ExtraHeaders[key] = resolvedValue
		}
	}

	return resolved, nil
}

// ResolveProviderNetworkConfig returns a shallow provider-config copy whose
// network configuration is resolved for runtime use. Nested configuration is
// intentionally shared because this function only replaces NetworkConfig.
func ResolveProviderNetworkConfig(config *ProviderConfig) (*ProviderConfig, error) {
	if config == nil {
		return nil, fmt.Errorf("provider config is nil")
	}
	resolvedNetworkConfig, err := ResolveNetworkConfig(config.NetworkConfig)
	if err != nil {
		return nil, err
	}
	resolved := *config
	resolved.NetworkConfig = resolvedNetworkConfig
	return &resolved, nil
}
