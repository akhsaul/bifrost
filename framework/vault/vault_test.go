package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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
		case "BIFROST_CONFIG_KEYS_KEY_1_VALUE":
			// Readback after StoreVaultSecretVar (store-then-fetch round trip).
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name:  "BIFROST_CONFIG_KEYS_KEY_1_VALUE",
				Value: DopplerSecretItem{Computed: "my-plain-secret"},
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

func TestVaultManager_ResolveAutoManagedPathPrefersStoreTarget(t *testing.T) {
	var mu sync.Mutex
	var getNames []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v3/configs/config/secret" {
			name := r.URL.Query().Get("name")
			mu.Lock()
			getNames = append(getNames, name)
			mu.Unlock()
			if name == "BF_CONFIG_KEYS_7047594D_498A_4700_BE0D_C38A4CD7D37A_VALUE" {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
					Name:  name,
					Value: DopplerSecretItem{Computed: "«redacted:sk-…»"},
				})
				return
			}
			// Real Doppler behavior for unknown names: 200 + null values.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": name, "value": map[string]any{"raw": nil, "computed": nil}, "success": true,
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "test-token"})
	}))
	defer server.Close()

	cfg := &Config{
		Enabled:    true,
		Type:       VaultTypeDoppler,
		Prefix:     "bf",
		AccessMode: AccessModeReadOnly,
		Doppler: &DopplerConfig{
			Token:   schemas.NewSecretVar("dp.st.test"),
			Project: schemas.NewSecretVar("main-proj"),
			Config:  schemas.NewSecretVar("dev"),
			BaseURL: schemas.NewSecretVar(server.URL),
		},
	}

	mgr, err := InitVaultManager(cfg, nil)
	require.NoError(t, err)
	defer mgr.Close()

	val, err := mgr.Resolve(context.Background(), "vault.bf/config_keys/7047594d-498a-4700-be0d-c38a4cd7d37a/value")
	require.NoError(t, err)
	assert.Equal(t, "«redacted:sk-…»", val)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, getNames)
	assert.Equal(t, "BF_CONFIG_KEYS_7047594D_498A_4700_BE0D_C38A4CD7D37A_VALUE", getNames[0],
		"first lookup must mirror resolveStoreTarget's normalized name")
}

func TestVaultManager_ResolveDoesNotCacheEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": r.URL.Query().Get("name"), "value": map[string]any{"raw": nil, "computed": nil}, "success": true,
		})
	}))
	defer server.Close()

	cfg := &Config{
		Enabled:    true,
		Type:       VaultTypeDoppler,
		Prefix:     "bifrost",
		AccessMode: AccessModeReadOnly,
		Doppler: &DopplerConfig{
			Token:   schemas.NewSecretVar("dp.st.test"),
			Project: schemas.NewSecretVar("main-proj"),
			Config:  schemas.NewSecretVar("dev"),
			BaseURL: schemas.NewSecretVar(server.URL),
		},
	}

	mgr, err := InitVaultManager(cfg, nil)
	require.NoError(t, err)
	defer mgr.Close()

	_, err = mgr.Resolve(context.Background(), "vault.MISSING_SECRET")
	assert.ErrorIs(t, err, ErrSecretNotFound)

	_, found := mgr.getFromCache("MISSING_SECRET")
	assert.False(t, found, "empty resolve results must not be cached")
}

func TestVaultManager_StoreStringVerifiesReadback(t *testing.T) {
	secretName := "BIFROST_CONFIG_KEYS_KEY_1_VALUE"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/configs/config/secrets":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/configs/config/secret":
			if r.URL.Query().Get("name") != secretName {
				// 200 + null for unknown names (real Doppler behavior).
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"name":    r.URL.Query().Get("name"),
					"value":   map[string]any{"raw": nil, "computed": nil},
					"success": true,
				})
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name:  secretName,
				Value: DopplerSecretItem{Computed: "sk-from-doppler"},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "test-token"})
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

	// Happy path: store succeeds and the cache holds the value READ BACK from
	// the vault, not the local plaintext.
	val := "sk-plain-new-key"
	require.NoError(t, mgr.StoreString(context.Background(), "bifrost/config_keys/key_1/value", &val))
	assert.Equal(t, "vault.bifrost/config_keys/key_1/value", val)
	cached, found := mgr.getFromCache("bifrost/config_keys/key_1/value")
	assert.True(t, found)
	assert.Equal(t, "sk-from-doppler", cached, "cache must hold the vault readback value")

	// Failure path: readback empty -> StoreString must fail loudly so a broken
	// ref never reaches the database.
	val2 := "sk-second"
	err = mgr.StoreString(context.Background(), "bifrost/config_keys/key_2/value", &val2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "post-store readback")
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
