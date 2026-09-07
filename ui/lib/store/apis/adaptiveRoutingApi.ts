/**
 * Adaptive Routing RTK Query API
 * Connects the Adaptive Routing dashboard and settings views to live runtime telemetry and configuration.
 */

import { baseApi } from "./baseApi";
import { AdaptiveRoutingConfig, AdaptiveRoutingMetricsResponse } from "@/lib/types/adaptiveRouting";

export const adaptiveRoutingApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Poll real-time EWMA, TTFT, P90, and dynamic weight telemetry
		getAdaptiveRoutingMetrics: builder.query<AdaptiveRoutingMetricsResponse, void>({
			query: () => ({
				url: "/adaptive-routing/metrics",
				method: "GET",
			}),
			providesTags: ["AdaptiveRoutingMetrics"],
		}),

		// Fetch current tuning parameters (alpha, epsilon floor, penalty multipliers)
		getAdaptiveRoutingConfig: builder.query<AdaptiveRoutingConfig, void>({
			query: () => ({
				url: "/adaptive-routing/config",
				method: "GET",
			}),
			providesTags: ["AdaptiveRoutingConfig"],
		}),

		// Apply and persist updated tuning parameters
		updateAdaptiveRoutingConfig: builder.mutation<AdaptiveRoutingConfig, Partial<AdaptiveRoutingConfig>>({
			query: (body) => ({
				url: "/adaptive-routing/config",
				method: "PUT",
				body,
			}),
			invalidatesTags: ["AdaptiveRoutingConfig", "AdaptiveRoutingMetrics"],
		}),
	}),
});

export const { useGetAdaptiveRoutingMetricsQuery, useGetAdaptiveRoutingConfigQuery, useUpdateAdaptiveRoutingConfigMutation } =
	adaptiveRoutingApi;