package handlers

import (
	"context"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vault"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// VaultHandler handles HTTP requests related to Vault and external secret managers.
type VaultHandler struct {
	config *lib.Config
}

// NewVaultHandler creates a new VaultHandler.
func NewVaultHandler(config *lib.Config) *VaultHandler {
	return &VaultHandler{
		config: config,
	}
}

// RegisterRoutes registers the vault-related routes.
func (h *VaultHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/vault/doppler/status", lib.ChainMiddlewares(h.getDopplerStatus, middlewares...))
	r.POST("/vault/flush-cache", lib.ChainMiddlewares(h.flushCache, middlewares...))
}

// getDopplerStatus handles GET /api/vault/doppler/status.
func (h *VaultHandler) getDopplerStatus(ctx *fasthttp.RequestCtx) {
	manager := vault.GetActiveManager()
	if manager == nil || manager.GetConfig() == nil || !manager.GetConfig().Enabled {
		SendJSON(ctx, map[string]any{
			"enabled": false,
			"message": "vault store is not enabled",
		})
		return
	}

	cfg := manager.GetConfig()
	resp := map[string]any{
		"enabled":     true,
		"type":        string(cfg.Type),
		"prefix":      cfg.GetPrefix(),
		"access_mode": string(cfg.GetAccessMode()),
	}

	if cfg.Type == vault.VaultTypeDoppler {
		dopplerProvider, ok := manager.GetProvider().(*vault.DopplerProvider)
		if ok && dopplerProvider != nil {
			resp["project"] = dopplerProvider.Project()
			resp["config"] = dopplerProvider.Config()
			resp["base_url"] = dopplerProvider.BaseURL()

			reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			authInfo, err := dopplerProvider.GetAuthInfo(reqCtx)
			if err == nil && authInfo != nil {
				resp["connected"] = true
				resp["authenticated_entity"] = authInfo
			} else {
				pingErr := dopplerProvider.Ping(reqCtx)
				resp["connected"] = (pingErr == nil)
				if pingErr != nil {
					resp["error"] = pingErr.Error()
				}
			}
		}
	}

	SendJSON(ctx, resp)
}

// flushCache handles POST /api/vault/flush-cache.
func (h *VaultHandler) flushCache(ctx *fasthttp.RequestCtx) {
	manager := vault.GetActiveManager()
	if manager == nil || manager.GetConfig() == nil || !manager.GetConfig().Enabled {
		SendError(ctx, fasthttp.StatusBadRequest, "vault is not enabled")
		return
	}

	manager.FlushCache()
	SendJSON(ctx, map[string]any{
		"message": "vault cache flushed",
		"success": true,
	})
}
