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

func TestDopplerProvider_GetSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer dp.st.test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "/v3/configs/config/secret", r.URL.Path)

		project := r.URL.Query().Get("project")
		config := r.URL.Query().Get("config")
		name := r.URL.Query().Get("name")

		if name == "OPENAI_API_KEY" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name: "OPENAI_API_KEY",
				Value: DopplerSecretItem{
					Raw:      "sk-test-raw",
					Computed: "sk-test-computed",
				},
			})
			return
		}

		if name == "RAW_ONLY" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(DopplerSecretSingleResponse{
				Name: "RAW_ONLY",
				Value: DopplerSecretItem{
					Raw: "raw-val",
				},
			})
			return
		}

		if name == "UNAUTHORIZED" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(DopplerErrorResponse{
				Messages: []string{"Invalid token"},
			})
			return
		}

		if name == "RATE_LIMITED" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(DopplerErrorResponse{
			Messages: []string{project + "/" + config + "/" + name + " not found"},
		})
	}))
	defer server.Close()

	cfg := &DopplerConfig{
		Token:   schemas.NewSecretVar("dp.st.test-token"),
		Project: schemas.NewSecretVar("test-proj"),
		Config:  schemas.NewSecretVar("prd"),
		BaseURL: schemas.NewSecretVar(server.URL),
	}

	provider, err := NewDopplerProvider(cfg)
	require.NoError(t, err)
	defer provider.Close()

	ctx := context.Background()

	// 1. Success with computed
	val, err := provider.GetSecret(ctx, "", "", "OPENAI_API_KEY")
	require.NoError(t, err)
	assert.Equal(t, "sk-test-computed", val)

	// 2. Success with raw only
	valRaw, err := provider.GetSecret(ctx, "", "", "RAW_ONLY")
	require.NoError(t, err)
	assert.Equal(t, "raw-val", valRaw)

	// 3. Not Found
	_, err = provider.GetSecret(ctx, "", "", "NON_EXISTENT")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSecretNotFound)

	// 4. Unauthorized
	_, err = provider.GetSecret(ctx, "", "", "UNAUTHORIZED")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorized)

	// 5. Rate limited
	_, err = provider.GetSecret(ctx, "", "", "RATE_LIMITED")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimited)
}

func TestDopplerProvider_ListSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/configs/config/secrets", r.URL.Path)
		assert.Equal(t, "test-proj", r.URL.Query().Get("project"))
		assert.Equal(t, "prd", r.URL.Query().Get("config"))

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DopplerSecretsListResponse{
			Secrets: map[string]DopplerSecretItem{
				"OPENAI_API_KEY": {
					Computed: "sk-openai",
				},
				"ANTHROPIC_API_KEY": {
					Raw:      "sk-ant-raw",
					Computed: "sk-ant-computed",
				},
			},
		})
	}))
	defer server.Close()

	cfg := &DopplerConfig{
		Token:   schemas.NewSecretVar("dp.st.test-token"),
		Project: schemas.NewSecretVar("test-proj"),
		Config:  schemas.NewSecretVar("prd"),
		BaseURL: schemas.NewSecretVar(server.URL),
	}

	provider, err := NewDopplerProvider(cfg)
	require.NoError(t, err)
	defer provider.Close()

	secrets, err := provider.ListSecrets(context.Background(), "", "")
	require.NoError(t, err)
	assert.Len(t, secrets, 2)
	assert.Equal(t, "sk-openai", secrets["OPENAI_API_KEY"])
	assert.Equal(t, "sk-ant-computed", secrets["ANTHROPIC_API_KEY"])
}

func TestDopplerProvider_SetSecret(t *testing.T) {
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v3/configs/config/secrets", r.URL.Path)

		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer server.Close()

	cfg := &DopplerConfig{
		Token:   schemas.NewSecretVar("dp.st.test-token"),
		Project: schemas.NewSecretVar("my-proj"),
		Config:  schemas.NewSecretVar("dev"),
		BaseURL: schemas.NewSecretVar(server.URL),
	}

	provider, err := NewDopplerProvider(cfg)
	require.NoError(t, err)
	defer provider.Close()

	err = provider.SetSecret(context.Background(), "my-proj", "dev", "NEW_KEY", "new-value")
	require.NoError(t, err)

	secretsMap, ok := receivedBody["secrets"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "new-value", secretsMap["NEW_KEY"])
}

func TestDopplerProvider_DeleteSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/v3/configs/config/secret", r.URL.Path)
		assert.Equal(t, "SECRET_TO_DELETE", r.URL.Query().Get("name"))

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer server.Close()

	cfg := &DopplerConfig{
		Token:   schemas.NewSecretVar("dp.st.test-token"),
		Project: schemas.NewSecretVar("my-proj"),
		Config:  schemas.NewSecretVar("dev"),
		BaseURL: schemas.NewSecretVar(server.URL),
	}

	provider, err := NewDopplerProvider(cfg)
	require.NoError(t, err)
	defer provider.Close()

	err = provider.DeleteSecret(context.Background(), "", "", "SECRET_TO_DELETE")
	require.NoError(t, err)
}

func TestDopplerProvider_Ping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer valid" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "test-token"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	cfgValid := &DopplerConfig{
		Token:   schemas.NewSecretVar("valid"),
		BaseURL: schemas.NewSecretVar(server.URL),
	}
	pValid, err := NewDopplerProvider(cfgValid)
	require.NoError(t, err)
	defer pValid.Close()
	assert.NoError(t, pValid.Ping(context.Background()))

	cfgInvalid := &DopplerConfig{
		Token:   schemas.NewSecretVar("invalid"),
		BaseURL: schemas.NewSecretVar(server.URL),
	}
	pInvalid, err := NewDopplerProvider(cfgInvalid)
	require.NoError(t, err)
	defer pInvalid.Close()
	assert.ErrorIs(t, pInvalid.Ping(context.Background()), ErrUnauthorized)
}
