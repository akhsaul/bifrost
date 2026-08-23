package circuitbreaker

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "bifrost-circuit-breaker"
)

// KeyModelConfig defines static cooldown configuration for a specific key and/or model.
type KeyModelConfig struct {
	// KeyID specifies the key ID this configuration applies to (empty means any key).
	KeyID string `json:"key_id,omitempty"`
	// Model specifies the model identifier this configuration applies to (empty means any model).
	Model string `json:"model,omitempty"`
	// Provider specifies the provider this configuration applies to (empty means any provider).
	Provider schemas.ModelProvider `json:"provider,omitempty"`
	// Cooldown defines the cooldown duration when tripped.
	Cooldown schemas.Duration `json:"cooldown"`
}

// Config defines the configuration for the Circuit Breaker plugin.
type Config struct {
	// Enabled determines whether the circuit breaker plugin is active.
	Enabled bool `json:"enabled"`
	// DefaultCooldown is the fallback cooldown duration (e.g. 5 hours) if no error-specified reset
	// time or specific key/model cooldown is matched.
	DefaultCooldown schemas.Duration `json:"default_cooldown"`
	// KeyConfigs allows setting custom cooldown durations per key, model, or provider.
	KeyConfigs []KeyModelConfig `json:"key_configs,omitempty"`
	// ErrorPatterns allows specifying custom regex or substring patterns to detect rate limit/quota errors.
	ErrorPatterns []string `json:"error_patterns,omitempty"`
}

// BreakerStatus represents the current trip status of a (Key, Model).
type BreakerStatus struct {
	KeyID      string                `json:"key_id"`
	Model      string                `json:"model"`
	Provider   schemas.ModelProvider `json:"provider"`
	TrippedAt  time.Time             `json:"tripped_at"`
	ResetTime  time.Time             `json:"reset_time"`
	Reason     string                `json:"reason"`
	StatusCode int                   `json:"status_code"`
}

// IsActive returns true if the breaker is currently in tripped cooldown period.
func (s *BreakerStatus) IsActive(now time.Time) bool {
	return now.Before(s.ResetTime)
}
