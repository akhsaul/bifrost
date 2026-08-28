package handlers

import (
	"encoding/json"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vault"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

func TestVaultHandler_RegisterRoutes(t *testing.T) {
	r := router.New()
	h := NewVaultHandler(nil)
	h.RegisterRoutes(r)

	// Verify route registration for /api/vault/doppler/status
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.SetRequestURI("/api/vault/doppler/status")
	reqCtx.Request.Header.SetMethod(fasthttp.MethodGet)

	handler, _ := r.Lookup("GET", "/api/vault/doppler/status", reqCtx)
	assert.NotNil(t, handler, "expected /api/vault/doppler/status route to be registered")

	handlerPost, _ := r.Lookup("POST", "/api/vault/flush-cache", reqCtx)
	assert.NotNil(t, handlerPost, "expected /api/vault/flush-cache route to be registered")
}

func TestVaultHandler_GetDopplerStatus_Disabled(t *testing.T) {
	h := NewVaultHandler(nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodGet)
	h.getDopplerStatus(ctx)

	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	var resp map[string]any
	err := json.Unmarshal(ctx.Response.Body(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, false, resp["enabled"])
}

func TestVaultHandler_GetDopplerStatus_Enabled(t *testing.T) {
	// Initialize vault manager with doppler config
	cfg := &vault.Config{
		Enabled:    true,
		Type:       vault.VaultTypeDoppler,
		Prefix:     "bifrost",
		AccessMode: vault.AccessModeReadOnly,
		Doppler: &vault.DopplerConfig{
			Token:   schemas.NewSecretVar("dp.pt.test-token"),
			Project: schemas.NewSecretVar("test-project"),
			Config:  schemas.NewSecretVar("prd"),
		},
	}
	_, _ = vault.InitVaultManager(cfg, nil)

	h := NewVaultHandler(nil)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodGet)
	h.getDopplerStatus(ctx)

	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	var resp map[string]any
	err := json.Unmarshal(ctx.Response.Body(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, true, resp["enabled"])
	assert.Equal(t, "doppler", resp["type"])
	assert.Equal(t, "test-project", resp["project"])
	assert.Equal(t, "prd", resp["config"])
}
