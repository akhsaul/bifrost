package adaptiverouting

import (
	"math"
)

// CalculateCompositeScore computes effective latency and composite score for a target.
func CalculateCompositeScore(stats TargetStats, config Config) (effectiveLatency float64, score float64) {
	// Baseline latency (default 200ms if no data yet to allow fresh exploration)
	baseLatency := stats.EWMALatencyMs
	if baseLatency <= 0 {
		baseLatency = 200.0
	}

	// Error & 429 penalty multiplier
	penalty := 1.0
	if stats.TotalRequests > 0 {
		rate429 := float64(stats.RateLimit429Count) / float64(stats.TotalRequests)
		rate5xx := float64(stats.Error5xxCount) / float64(stats.TotalRequests)

		penalty += (config.Penalty429 * rate429) + (config.Penalty5xx * rate5xx)
	}

	effectiveLatency = baseLatency * penalty
	if effectiveLatency < 1.0 {
		effectiveLatency = 1.0
	}

	// Score is proportional to inverse latency
	score = 1000.0 / effectiveLatency
	return effectiveLatency, score
}

// ComputeDynamicWeights takes a list of candidate targets and their stats, and produces normalized dynamic weights.
func ComputeDynamicWeights(candidates []TargetID, statsMap map[TargetID]TargetStats, config Config) []TargetWeight {
	n := len(candidates)
	if n == 0 {
		return nil
	}
	if n == 1 {
		effLat, score := CalculateCompositeScore(statsMap[candidates[0]], config)
		return []TargetWeight{
			{
				TargetID:  candidates[0],
				Weight:    1.0,
				CumWeight: 1.0,
				Score:     score,
				P90Ms:     effLat,
			},
		}
	}

	rawWeights := make([]float64, n)
	scores := make([]float64, n)
	effectiveLatencies := make([]float64, n)
	totalRawWeight := 0.0

	powerK := config.PowerK
	if powerK <= 0 {
		powerK = 1.5
	}

	for i, candidate := range candidates {
		stats := statsMap[candidate]
		effLat, score := CalculateCompositeScore(stats, config)
		effectiveLatencies[i] = effLat
		scores[i] = score

		// Inverse-latency formula: (1 / latency)^powerK
		rawW := math.Pow(1000.0/effLat, powerK)
		rawWeights[i] = rawW
		totalRawWeight += rawW
	}

	eps := config.ExplorationFloor
	if eps < 0 {
		eps = 0.0
	}
	// Exploration floor cannot exceed 1/N
	if eps*float64(n) >= 1.0 {
		eps = 1.0 / (float64(n) * 2.0)
	}

	results := make([]TargetWeight, n)
	cum := 0.0

	for i, candidate := range candidates {
		var normW float64
		if totalRawWeight > 0 {
			normW = (1.0 - float64(n)*eps)*(rawWeights[i]/totalRawWeight) + eps
		} else {
			normW = 1.0 / float64(n)
		}

		cum += normW
		results[i] = TargetWeight{
			TargetID:  candidate,
			Weight:    normW,
			CumWeight: cum,
			Score:     scores[i],
			P90Ms:     effectiveLatencies[i],
		}
	}

	// Guarantee last cumulative weight is 1.0 to avoid floating point precision issues
	if len(results) > 0 {
		results[len(results)-1].CumWeight = 1.0
	}

	return results
}
