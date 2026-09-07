package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/plugins/adaptiverouting"
	"github.com/valyala/fasthttp"
)

// TestAdaptiveRoutingHandler_GetMetrics_EmptyWhenPluginNil verifies that when the plugin
// is disabled or not loaded, GET /api/adaptive-routing/metrics still returns 200 with an
// empty metrics array (never 500, never null).
func TestAdaptiveRoutingHandler_GetMetrics_EmptyWhenPluginNil(t *testing.T) {
	h := NewAdaptiveRoutingHandler(func() *adaptiverouting.Plugin { return nil }, nil)

	ctx := &fasthttp.RequestCtx{}
	h.getMetrics(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status = %d, want %d", ctx.Response.StatusCode(), fasthttp.StatusOK)
	}

	var decoded adaptiverouting.AdaptiveMetricsSummary
	if err := json.Unmarshal(ctx.Response.Body(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", string(ctx.Response.Body()), err)
	}
	if decoded.Metrics == nil {
		t.Errorf("metrics must be an empty array, got null")
	}
	if decoded.Summary.TotalTargets != 0 {
		t.Errorf("total_targets = %d, want 0", decoded.Summary.TotalTargets)
	}
}

// TestAdaptiveRoutingHandler_GetConfig_DefaultWhenPluginNil verifies the config endpoint
// falls back to the default configuration when the plugin is not loaded.
func TestAdaptiveRoutingHandler_GetConfig_DefaultWhenPluginNil(t *testing.T) {
	h := NewAdaptiveRoutingHandler(func() *adaptiverouting.Plugin { return nil }, nil)

	ctx := &fasthttp.RequestCtx{}
	h.getConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status = %d, want %d", ctx.Response.StatusCode(), fasthttp.StatusOK)
	}

	var decoded adaptiverouting.Config
	if err := json.Unmarshal(ctx.Response.Body(), &decoded); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	want := adaptiverouting.DefaultConfig()
	if decoded.Alpha != want.Alpha || decoded.PowerK != want.PowerK {
		t.Errorf("config = %+v, want defaults %+v", decoded, want)
	}
}

// TestAdaptiveRoutingHandler_GetConfig_WhenPluginLoaded verifies the config endpoint
// returns the plugin's live configuration when the plugin is present.
func TestAdaptiveRoutingHandler_GetConfig_WhenPluginLoaded(t *testing.T) {
	cfg := adaptiverouting.DefaultConfig()
	cfg.Alpha = 0.42
	p, err := adaptiverouting.New(cfg, nil, nil)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}
	defer func() { _ = p.Cleanup() }()

	h := NewAdaptiveRoutingHandler(func() *adaptiverouting.Plugin { return p }, nil)

	ctx := &fasthttp.RequestCtx{}
	h.getConfig(ctx)

	var decoded adaptiverouting.Config
	if err := json.Unmarshal(ctx.Response.Body(), &decoded); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if decoded.Alpha != 0.42 {
		t.Errorf("alpha = %v, want 0.42", decoded.Alpha)
	}
}

// TestAdaptiveRoutingHandler_UpdateConfig verifies PUT applies the new configuration to
// the live plugin and echoes it back.
func TestAdaptiveRoutingHandler_UpdateConfig(t *testing.T) {
	p, err := adaptiverouting.New(adaptiverouting.DefaultConfig(), nil, nil)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}
	defer func() { _ = p.Cleanup() }()

	h := NewAdaptiveRoutingHandler(func() *adaptiverouting.Plugin { return p }, nil)

	newCfg := adaptiverouting.DefaultConfig()
	newCfg.ExplorationFloor = 0.1
	newCfg.Penalty429 = 8.0
	body, _ := json.Marshal(newCfg)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.Request.SetBody(body)
	h.updateConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", ctx.Response.StatusCode(), fasthttp.StatusOK, string(ctx.Response.Body()))
	}

	got := p.GetConfig()
	if got.ExplorationFloor != 0.1 || got.Penalty429 != 8.0 {
		t.Errorf("plugin config after update = %+v, want exploration_floor=0.1 penalty_429=8.0", got)
	}
}

// TestAdaptiveRoutingHandler_UpdateConfig_InvalidBody verifies a malformed body is rejected
// with 400 and the plugin config remains untouched.
func TestAdaptiveRoutingHandler_UpdateConfig_InvalidBody(t *testing.T) {
	p, err := adaptiverouting.New(adaptiverouting.DefaultConfig(), nil, nil)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}
	defer func() { _ = p.Cleanup() }()

	h := NewAdaptiveRoutingHandler(func() *adaptiverouting.Plugin { return p }, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.Request.SetBody([]byte("{invalid-json"))
	h.updateConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want %d", ctx.Response.StatusCode(), fasthttp.StatusBadRequest)
	}
	if p.GetConfig().ExplorationFloor != adaptiverouting.DefaultConfig().ExplorationFloor {
		t.Errorf("plugin config must be untouched on invalid request")
	}
}

// TestAdaptiveRoutingHandler_MetricsShape_WhenPluginLoaded pins the exact JSON contract
// consumed by the dashboard: metrics entries carry target/provider/model fields.
func TestAdaptiveRoutingHandler_MetricsShape_WhenPluginLoaded(t *testing.T) {
	p, err := adaptiverouting.New(adaptiverouting.DefaultConfig(), nil, nil)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}
	defer func() { _ = p.Cleanup() }()

	// Record a sample via the public Store so the summary has something to expose
	p.GetStore().RecordMetric(context.Background(), adaptiverouting.TargetID{
		Provider: "openai",
		Model:    "gpt-4o",
	}, 50_000_000 /* 50ms */, 20_000_000 /* 20ms TTFT */, 200, false)

	h := NewAdaptiveRoutingHandler(func() *adaptiverouting.Plugin { return p }, nil)

	ctx := &fasthttp.RequestCtx{}
	h.getMetrics(ctx)

	var decoded struct {
		Metrics []struct {
			Target        string  `json:"target"`
			Provider      string  `json:"provider"`
			Model         string  `json:"model"`
			EWMALatencyMs float64 `json:"ewma_latency_ms"`
			TotalRequests int64   `json:"total_requests"`
		} `json:"metrics"`
		Summary struct {
			TotalTargets int `json:"total_targets"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &decoded); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if len(decoded.Metrics) != 1 {
		t.Fatalf("metrics length = %d, want 1; body: %s", len(decoded.Metrics), string(ctx.Response.Body()))
	}
	m := decoded.Metrics[0]
	if m.Target != "openai/gpt-4o" || m.Provider != "openai" || m.Model != "gpt-4o" {
		t.Errorf("metric = %+v, want target openai/gpt-4o", m)
	}
	if m.TotalRequests != 1 {
		t.Errorf("total_requests = %d, want 1", m.TotalRequests)
	}
	if decoded.Summary.TotalTargets != 1 {
		t.Errorf("summary.total_targets = %d, want 1", decoded.Summary.TotalTargets)
	}
}
