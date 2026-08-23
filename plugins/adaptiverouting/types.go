package adaptiverouting

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "bifrost-adaptive-routing"
)

// TargetID uniquely identifies an upstream target (provider, model, and optional key).
type TargetID struct {
	Provider schemas.ModelProvider `json:"provider"`
	Model    string                `json:"model"`
	KeyID    string                `json:"key_id,omitempty"`
}

func (t TargetID) String() string {
	if t.KeyID != "" {
		return fmt.Sprintf("%s/%s#%s", t.Provider, t.Model, t.KeyID)
	}
	return fmt.Sprintf("%s/%s", t.Provider, t.Model)
}

// TargetStats contains real-time performance metrics and scores for a specific TargetID.
type TargetStats struct {
	TargetID          TargetID  `json:"target_id"`
	EWMALatencyMs     float64   `json:"ewma_latency_ms"`
	TTFTMs            float64   `json:"ttft_ms"`
	P90LatencyMs      float64   `json:"p90_latency_ms"`
	SuccessCount      int64     `json:"success_count"`
	RateLimit429Count int64     `json:"rate_limit_429_count"`
	Error5xxCount     int64     `json:"error_5xx_count"`
	TotalRequests     int64     `json:"total_requests"`
	EffectiveScore    float64   `json:"effective_score"`
	LastObservedAt    time.Time `json:"last_observed_at"`
}

// TargetWeight represents the calculated routing weight for a candidate target.
type TargetWeight struct {
	TargetID  TargetID `json:"target_id"`
	Weight    float64  `json:"weight"`
	CumWeight float64  `json:"cum_weight"`
	Score     float64  `json:"score"`
	P90Ms     float64  `json:"p90_ms"`
}

// AdaptiveRoutingSnapshot represents an immutable routing snapshot for zero-lock reads.
type AdaptiveRoutingSnapshot struct {
	// Weights maps a group/pool key to a slice of weighted candidate targets.
	Weights   map[string][]TargetWeight `json:"weights"`
	UpdatedAt time.Time                 `json:"updated_at"`
}

// Config defines the configuration for the Adaptive Routing plugin.
type Config struct {
	// Enabled determines whether the adaptive routing plugin is active.
	Enabled bool `json:"enabled"`
	// ExplorationFloor is the minimum traffic share (epsilon, e.g. 0.05 for 5%) allocated to degraded routes.
	ExplorationFloor float64 `json:"exploration_floor"`
	// Alpha is the decay factor for EWMA latency calculation (0.0 < alpha <= 1.0, default 0.2).
	Alpha float64 `json:"alpha"`
	// PowerK is the inverse latency exponent (default 1.5).
	PowerK float64 `json:"power_k"`
	// Penalty429 is the multiplier for 429 rate limits (default 5.0).
	Penalty429 float64 `json:"penalty_429"`
	// Penalty5xx is the multiplier for 5xx server errors (default 10.0).
	Penalty5xx float64 `json:"penalty_5xx"`
	// TuningInterval is how frequently active routing weights are recalculated in the background.
	TuningInterval schemas.Duration `json:"tuning_interval"`
	// WindowSize defines the rolling metrics evaluation window.
	WindowSize schemas.Duration `json:"window_size"`
}

// DefaultConfig returns the standard recommended default configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		ExplorationFloor: 0.05,
		Alpha:            0.2,
		PowerK:           1.5,
		Penalty429:       5.0,
		Penalty5xx:       10.0,
		TuningInterval:   schemas.Duration(3 * time.Second),
		WindowSize:       schemas.Duration(5 * time.Minute),
	}
}
