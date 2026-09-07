/**
 * Adaptive Routing types
 * Mirrors plugins/adaptiverouting/types.go (TargetMetricView, AdaptiveMetricsSummary, Config)
 */

export type AdaptiveTargetStatus = "healthy" | "degraded" | "optimal";

export interface AdaptiveTargetMetric {
	target: string;
	provider: string;
	model: string;
	key_id?: string;
	ewma_latency_ms: number;
	ttft_ms: number;
	p90_latency_ms: number;
	success_rate: number;
	rate_limit_count: number;
	error_count: number;
	total_requests: number;
	dynamic_weight: number;
	status: AdaptiveTargetStatus;
}

export interface AdaptiveRoutingMetricsResponse {
	metrics: AdaptiveTargetMetric[];
	summary: {
		avg_ewma_latency_ms: number;
		avg_ttft_ms: number;
		total_targets: number;
		total_429s: number;
	};
}

export interface AdaptiveRoutingConfig {
	enabled: boolean;
	exploration_floor: number;
	alpha: number;
	power_k: number;
	penalty_429: number;
	penalty_5xx: number;
	tuning_interval?: string;
	window_size?: string;
}