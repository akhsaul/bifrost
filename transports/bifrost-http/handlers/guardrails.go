package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/guardrails"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"
)

type GuardrailsHandler struct {
	config        *lib.Config
	pluginsLoader PluginsLoader
}

func NewGuardrailsHandler(config *lib.Config, pluginsLoader PluginsLoader) *GuardrailsHandler {
	return &GuardrailsHandler{config: config, pluginsLoader: pluginsLoader}
}

func (h *GuardrailsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/config/guardrails", lib.ChainMiddlewares(h.getGuardrailsConfig, middlewares...))
	r.PUT("/api/config/guardrails", lib.ChainMiddlewares(h.updateGuardrailsConfig, middlewares...))
}

func (h *GuardrailsHandler) getGuardrailsConfig(ctx *fasthttp.RequestCtx) {
	h.config.Mu.RLock()
	config := h.config.GuardrailsConfig
	h.config.Mu.RUnlock()

	if config == nil {
		SendJSON(ctx, &guardrails.Config{})
		return
	}

	SendJSON(ctx, config)
}

func (h *GuardrailsHandler) updateGuardrailsConfig(ctx *fasthttp.RequestCtx) {
	var newConfig guardrails.Config
	if err := json.Unmarshal(ctx.PostBody(), &newConfig); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid payload: %v", err))
		return
	}

	// Persist to database if config store is enabled
	if h.config.ConfigStore != nil {
		configBytes, err := json.Marshal(newConfig)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to marshal config: %v", err))
			return
		}
		tableConfig := &tables.TableGovernanceConfig{
			Key:   tables.ConfigGuardrailsConfigKey,
			Value: string(configBytes),
		}
		if err := h.config.ConfigStore.UpdateConfig(ctx, tableConfig); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}
	}

	// Update memory
	h.config.Mu.Lock()
	h.config.GuardrailsConfig = &newConfig
	h.config.Mu.Unlock()

	// Trigger dynamic plugin reload across HTTP transport and Bifrost core
	if h.pluginsLoader != nil {
		if err := h.pluginsLoader.ReloadPlugin(ctx, guardrails.PluginName, nil, &newConfig, nil, nil); err != nil {
			logger.Error("failed to reload guardrails plugin: %v", err)
		}
	} else {
		plugin, err := guardrails.Init(&newConfig, logger)
		if err != nil {
			logger.Error("failed to init guardrails plugin after config update: %v", err)
		} else {
			h.config.ReloadPlugin(plugin)
		}
	}

	SendJSON(ctx, map[string]bool{"success": true})
}
