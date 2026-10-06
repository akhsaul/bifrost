package handlers

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestGetModelParameters_ResolvesQualifiedAndBareIDs(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()

	dbPath := t.TempDir() + "/config.db"
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: dbPath},
	}, &mockLogger{})
	require.NoError(t, err)

	require.NoError(t, store.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "gpt-5.5", Data: `{"model_parameters":[{"id":"reasoning_effort"}]}`},
		{Model: "openrouter/moonshotai/kimi-k2.5", Data: `{"model_parameters":[{"id":"temperature"}]}`},
		{Model: "claude-haiku-4-5", Data: `{"provider":"anthropic","model_parameters":[{"id":"temperature"},{"id":"top_p"}]}`},
		{Model: "zed/claude-haiku-4-5", Data: `{"provider":"zed","model_parameters":[{"id":"thinking"}]}`},
	}))

	ds := datasheet.New(store, &mockLogger{}, datasheet.Config{})
	rows, err := ds.LoadModelParamsFromDB(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, rows)

	h := &ProviderHandler{
		dbStore: store,
		inMemoryStore: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalogWithDatasheet(ds),
		},
	}

	tests := []struct {
		name       string
		model      string
		provider   string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "exact bare key",
			model:      "gpt-5.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"reasoning_effort"}]}`,
		},
		{
			name:       "provider-qualified resolves to bare key",
			model:      "openai/gpt-5.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"reasoning_effort"}]}`,
		},
		{
			name:       "openrouter double-qualified resolves to bare key",
			model:      "openrouter/openai/gpt-5.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"reasoning_effort"}]}`,
		},
		{
			name:       "bare alias resolves to openrouter-qualified key",
			model:      "kimi-k2.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"temperature"}]}`,
		},
		{
			name:       "unknown model still 404s",
			model:      "definitely-not-a-model",
			wantStatus: fasthttp.StatusNotFound,
		},
		{
			name:       "provider hint selects the gateway row over the base row",
			model:      "claude-haiku-4-5",
			provider:   "zed",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"provider":"zed","model_parameters":[{"id":"thinking"}]}`,
		},
		{
			name:       "provider hint selects the base row when asked",
			model:      "claude-haiku-4-5",
			provider:   "anthropic",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"provider":"anthropic","model_parameters":[{"id":"temperature"},{"id":"top_p"}]}`,
		},
		{
			name:       "qualified model with matching provider hint resolves",
			model:      "zed/claude-haiku-4-5",
			provider:   "zed",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"provider":"zed","model_parameters":[{"id":"thinking"}]}`,
		},
		{
			name:       "qualified model with mismatched hint is strict 404",
			model:      "zed/claude-haiku-4-5",
			provider:   "anthropic",
			wantStatus: fasthttp.StatusNotFound,
		},
		{
			name:       "provider hint with no row for that provider is strict 404",
			model:      "claude-haiku-4-5",
			provider:   "openai",
			wantStatus: fasthttp.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri := fmt.Sprintf("/api/models/parameters?model=%s", tt.model)
			if tt.provider != "" {
				uri += "&provider=" + tt.provider
			}
			var req fasthttp.Request
			req.SetRequestURI(uri)
			reqCtx := &fasthttp.RequestCtx{}
			reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

			h.getModelParameters(reqCtx)

			require.Equal(t, tt.wantStatus, reqCtx.Response.StatusCode())
			if tt.wantBody != "" {
				require.Equal(t, tt.wantBody, string(reqCtx.Response.Body()))
			}
		})
	}

	t.Run("nil inMemoryStore falls back to exact DB lookup", func(t *testing.T) {
		bare := &ProviderHandler{dbStore: store}

		var req fasthttp.Request
		req.SetRequestURI("/api/models/parameters?model=gpt-5.5")
		reqCtx := &fasthttp.RequestCtx{}
		reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

		bare.getModelParameters(reqCtx)

		require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode())
		require.Equal(t, `{"model_parameters":[{"id":"reasoning_effort"}]}`, string(reqCtx.Response.Body()))
	})
}
