// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains the adaptive routing real-time metrics and configuration endpoints
// that back the Adaptive Routing dashboard and settings UI.
package handlers

import (
	"context"
	"encoding/json"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/adaptiverouting"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AdaptiveRoutingHandler serves live EWMA telemetry and plugin configuration for the
// Adaptive Routing dashboard. The plugin is resolved per request (via pluginResolver)
// so plugin reloads through /api/plugins are honored instead of caching a stale pointer.
type AdaptiveRoutingHandler struct {
	pluginResolver func() *adaptiverouting.Plugin
	configStore    configstore.ConfigStore
}

// NewAdaptiveRoutingHandler creates the handler. pluginResolver may return nil when the
// plugin is disabled; endpoints then respond with defaults/empty payloads instead of 500.
func NewAdaptiveRoutingHandler(pluginResolver func() *adaptiverouting.Plugin, configStore configstore.ConfigStore) *AdaptiveRoutingHandler {
	return &AdaptiveRoutingHandler{
		pluginResolver: pluginResolver,
		configStore:    configStore,
	}
}

// RegisterRoutes registers the adaptive routing metrics and config routes.
func (h *AdaptiveRoutingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	register := func(method, path string, handler fasthttp.RequestHandler) {
		r.Handle(method, path, lib.ChainMiddlewares(handler, middlewares...))
	}

	// Canonical paths (also exposed under /api/v1/ for compatibility with the
	// original design doc in adaptive_routing.md).
	register(fasthttp.MethodGet, "/api/adaptive-routing/metrics", h.getMetrics)
	register(fasthttp.MethodGet, "/api/v1/adaptive-routing/metrics", h.getMetrics)
	register(fasthttp.MethodGet, "/api/adaptive-routing/config", h.getConfig)
	register(fasthttp.MethodGet, "/api/v1/adaptive-routing/config", h.getConfig)
	register(fasthttp.MethodPut, "/api/adaptive-routing/config", h.updateConfig)
	register(fasthttp.MethodPut, "/api/v1/adaptive-routing/config", h.updateConfig)
}

// getMetrics returns live EWMA / TTFT / P90 / weight telemetry for every monitored target.
func (h *AdaptiveRoutingHandler) getMetrics(ctx *fasthttp.RequestCtx) {
	p := h.pluginResolver()
	if p == nil {
		summary := adaptiverouting.AdaptiveMetricsSummary{
			Metrics: []adaptiverouting.TargetMetricView{},
		}
		SendJSON(ctx, summary)
		return
	}
	SendJSON(ctx, p.GetMetricsSummary())
}

// getConfig returns the current adaptive routing plugin configuration.
func (h *AdaptiveRoutingHandler) getConfig(ctx *fasthttp.RequestCtx) {
	p := h.pluginResolver()
	if p == nil {
		SendJSON(ctx, adaptiverouting.DefaultConfig())
		return
	}
	SendJSON(ctx, p.GetConfig())
}

// updateConfig applies and persists a new adaptive routing configuration.
func (h *AdaptiveRoutingHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	var req adaptiverouting.Config
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	p := h.pluginResolver()
	if p != nil {
		p.UpdateConfig(req)
	}

	if h.configStore != nil {
		if err := h.configStore.UpsertPlugin(context.Background(), &configstoreTables.TablePlugin{
			Name:    adaptiverouting.PluginName,
			Enabled: req.Enabled,
			Config:  req,
		}); err != nil {
			logger.Error("failed to persist adaptive routing config: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to persist config")
			return
		}
	}

	SendJSON(ctx, req)
}
