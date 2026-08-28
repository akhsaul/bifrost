package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultManager_ResolveAndHooks(t *testing.T) {
	// Mock Doppler server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v3/configs/config/secrets" {
			assert.Equal(t, "bifrost-app", r.URL.Query().Get("project"))
			assert.Equal(t, "prd", r.URL.Query().Get("config"))
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/v3/configs/config/secret" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
			return
		}

		name := r.URL.Query().Get("name")
		switch name {
		case "OPENAI_API_KEY":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name:  "OPENAI_API_KEY",
				Value: DopplerSecretItem{Computed: "sk-proj-12345"},
			})
		case "BIFROST_PROVIDERS_ANTHROPIC_KEY", "PROVIDERS_ANTHROPIC_KEY", "bifrost/providers/anthropic/key":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name:  "BIFROST_PROVIDERS_ANTHROPIC_KEY",
				Value: DopplerSecretItem{Computed: "sk-ant-secret"},
			})
		case "SHARED_CONFIG":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name: "SHARED_CONFIG",
				Value: DopplerSecretItem{
					Computed: `{"db_password": "super-secret-pwd", "api_key": "nested-api-key"}`,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(DopplerErrorResponse{Messages: []string{"not found"}})
		}
	}))
	defer server.Close()

	cfg := &Config{
		Enabled:    true,
		Type:       VaultTypeDoppler,
		Prefix:     "bifrost",
		AccessMode: AccessModeReadAndWrite,
		Doppler: &DopplerConfig{
			Token:   schemas.NewSecretVar("dp.st.test"),
			Project: schemas.NewSecretVar("bifrost-app"),
			Config:  schemas.NewSecretVar("prd"),
			BaseURL: schemas.NewSecretVar(server.URL),
		},
	}

	mgr, err := InitVaultManager(cfg, nil)
	require.NoError(t, err)
	defer mgr.Close()

	// 1. Resolve direct secret via LookupVault
	val, ok := schemas.LookupVault("vault.OPENAI_API_KEY")
	assert.True(t, ok)
	assert.Equal(t, "sk-proj-12345", val)

	// 2. Resolve via NewSecretVar
	sv := schemas.NewSecretVar("vault.OPENAI_API_KEY")
	assert.True(t, sv.IsFromVault())
	assert.Equal(t, "sk-proj-12345", sv.GetValue())

	// 3. Resolve normalized secret name (bifrost/providers/anthropic/key -> BIFROST_PROVIDERS_ANTHROPIC_KEY)
	svNormalized := schemas.NewSecretVar("vault.bifrost/providers/anthropic/key")
	assert.True(t, svNormalized.IsFromVault())
	assert.Equal(t, "sk-ant-secret", svNormalized.GetValue())

	// 4. Resolve JSON fragment
	svFragment := schemas.NewSecretVar("vault.SHARED_CONFIG#db_password")
	assert.True(t, svFragment.IsFromVault())
	assert.Equal(t, "super-secret-pwd", svFragment.GetValue())

	svFragmentKey := schemas.NewSecretVar("vault.SHARED_CONFIG#api_key")
	assert.True(t, svFragmentKey.IsFromVault())
	assert.Equal(t, "nested-api-key", svFragmentKey.GetValue())

	// 5. Caching test
	rawCached, found := mgr.getFromCache("OPENAI_API_KEY")
	assert.True(t, found)
	assert.Equal(t, "sk-proj-12345", rawCached)

	mgr.FlushCache()
	_, foundAfterFlush := mgr.getFromCache("OPENAI_API_KEY")
	assert.False(t, foundAfterFlush)

	// 6. StoreVaultSecretVar in ReadAndWrite mode
	newPlainSV := &schemas.SecretVar{Val: "my-plain-secret"}
	err = schemas.StoreVaultSecretVar(context.Background(), "bifrost/config_keys/key_1/value", newPlainSV)
	require.NoError(t, err)
	assert.True(t, newPlainSV.IsFromVault())
	assert.Equal(t, "vault.bifrost/config_keys/key_1/value", newPlainSV.GetRawRef())
}

func TestVaultManager_ReadOnlyRejectsStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "test-token"})
	}))
	defer server.Close()

	cfg := &Config{
		Enabled:    true,
		Type:       VaultTypeDoppler,
		Prefix:     "bifrost",
		AccessMode: AccessModeReadOnly,
		Doppler: &DopplerConfig{
			Token:   schemas.NewSecretVar("dp.st.test"),
			Project: schemas.NewSecretVar("bifrost-app"),
			Config:  schemas.NewSecretVar("prd"),
			BaseURL: schemas.NewSecretVar(server.URL),
		},
	}

	mgr, err := InitVaultManager(cfg, nil)
	require.NoError(t, err)
	defer mgr.Close()

	// In read-only mode, schemas.VaultStoreHook must be nil
	assert.Nil(t, schemas.VaultStoreHook)
	assert.False(t, schemas.VaultStoreWriteEnabled())

	val := "my-secret"
	err = mgr.StoreString(context.Background(), "path", &val)
	assert.ErrorIs(t, err, ErrReadOnlyMode)
}
