package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/compat"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// securityHeaders is the list of headers that cannot be configured in allowlist/denylist
// These headers are always blocked for security reasons regardless of user configuration
var securityHeaders = []string{
	"authorization",
	"proxy-authorization",
	"cookie",
	"host",
	"content-length",
	"connection",
	"transfer-encoding",
	"x-api-key",
	"x-goog-api-key",
	"x-bf-api-key",
	"x-bf-vk",
}

func getPasswordPolicyFailures(password string) []string {
	failures := make([]string, 0, 5)
	hasUppercase := false
	hasLowercase := false
	hasDigit := false
	hasSpecial := false

	for i := 0; i < len(password); i++ {
		char := password[i]
		switch {
		case char >= 'A' && char <= 'Z':
			hasUppercase = true
		case char >= 'a' && char <= 'z':
			hasLowercase = true
		case char >= '0' && char <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}

	if len(password) < 12 {
		failures = append(failures, "at least 12 characters")
	}
	if !hasUppercase {
		failures = append(failures, "one uppercase letter")
	}
	if !hasLowercase {
		failures = append(failures, "one lowercase letter")
	}
	if !hasDigit {
		failures = append(failures, "one number")
	}
	if !hasSpecial {
		failures = append(failures, "one special character")
	}

	return failures
}

// ConfigManager is the interface for the config manager
type ConfigManager interface {
	UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error
	ReloadClientConfigFromConfigStore(ctx context.Context) error
	UpdateSyncConfig(ctx context.Context) error
	ForceReloadPricing(ctx context.Context) error
	UpdateDropExcessRequests(ctx context.Context, value bool)
	UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string, disableAutoToolInject bool) error
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any, placement *schemas.PluginPlacement, order *int) error
	RemovePlugin(ctx context.Context, name string) error
	ReloadProxyConfig(ctx context.Context, config *configstoreTables.GlobalProxyConfig) error
	ReloadHeaderFilterConfig(ctx context.Context, config *configstoreTables.GlobalHeaderFilterConfig) error
}

// ConfigHandler manages runtime configuration updates for Bifrost.
// It provides endpoints to update and retrieve settings persisted via the ConfigStore backed by sql database.
type ConfigHandler struct {
	store         *lib.Config
	configManager ConfigManager
}

// NewConfigHandler creates a new handler for configuration management.
// It requires the Bifrost client, a logger, and the config store.
func NewConfigHandler(configManager ConfigManager, store *lib.Config) *ConfigHandler {
	return &ConfigHandler{
		configManager: configManager,
		store:         store,
	}
}

// RegisterRoutes registers the configuration-related routes.
// It adds the `PUT /api/config` endpoint.
func (h *ConfigHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.GET("/api/config/export", lib.ChainMiddlewares(h.exportConfig, middlewares...))
	r.PUT("/api/config", lib.ChainMiddlewares(h.updateConfig, middlewares...))
	r.POST("/api/config/metadata", lib.ChainMiddlewares(h.updateMetadata, middlewares...))
	r.GET("/api/version", lib.ChainMiddlewares(h.getVersion, middlewares...))
	r.GET("/api/proxy-config", lib.ChainMiddlewares(h.getProxyConfig, middlewares...))
	r.PUT("/api/proxy-config", lib.ChainMiddlewares(h.updateProxyConfig, middlewares...))
	r.POST("/api/pricing/force-sync", lib.ChainMiddlewares(h.forceSyncPricing, middlewares...))
}

// getVersion handles GET /api/version - Get the current version
func (h *ConfigHandler) getVersion(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, version)
}

// getConfig handles GET /config - Get the current configuration
func (h *ConfigHandler) getConfig(ctx *fasthttp.RequestCtx) {
	mapConfig := make(map[string]any)

	if query := string(ctx.QueryArgs().Peek("from_db")); query == "true" {
		if h.store.ConfigStore == nil {
			SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
			return
		}
		cc, err := h.store.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError,
				fmt.Sprintf("failed to fetch config from db: %v", err))
			return
		}
		if cc != nil {
			mapConfig["client_config"] = cc.Redacted()
		}
		// Fetching framework config
		fc, err := h.store.ConfigStore.GetFrameworkConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to fetch framework config from db: %v", err))
			return
		}
		normalizedFrameworkConfig, _, _ := lib.ResolveFrameworkPricingConfig(fc, nil)
		mapConfig["framework_config"] = *normalizedFrameworkConfig
	} else {
		mapConfig["client_config"] = h.store.ClientConfig.Redacted()
		// Snapshot under the read lock; updateConfig swaps this pointer from
		// another request goroutine.
		h.store.Mu.RLock()
		storedFrameworkConfig := h.store.FrameworkConfig
		h.store.Mu.RUnlock()
		normalizedFrameworkConfig, _, _ := lib.ResolveFrameworkPricingConfig(nil, storedFrameworkConfig)
		mapConfig["framework_config"] = *normalizedFrameworkConfig
	}
	if h.store.ConfigStore != nil {
		// Fetching governance config
		authConfig, err := h.store.ConfigStore.GetAuthConfig(ctx)
		if err != nil {
			logger.Warn("failed to get auth config from store: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get auth config from store: %v", err))
			return
		}
		// Getting username and password from auth config
		// This username password is for the dashboard authentication
		if authConfig != nil {
			// For password, return SecretVar structure with redacted value
			// If from env, preserve env_var reference but clear value
			// If not from env, show <redacted> as the value
			var passwordSecretVar *schemas.SecretVar
			if authConfig.AdminPassword != nil && authConfig.AdminPassword.IsFromSecret() {
				passwordSecretVar = authConfig.AdminPassword.FullyRedacted()
			} else {
				passwordSecretVar = &schemas.SecretVar{
					Val: "<redacted>",
				}
			}
			mapConfig["auth_config"] = map[string]any{
				"admin_username": authConfig.AdminUserName,
				"admin_password": passwordSecretVar,
				"is_enabled":     authConfig.IsEnabled,
			}
		} else {
			// No auth config exists yet, return default empty SecretVar values
			mapConfig["auth_config"] = map[string]any{
				"admin_username": &schemas.SecretVar{},
				"admin_password": &schemas.SecretVar{},
				"is_enabled":     false,
			}
		}
	} else {
		mapConfig["auth_config"] = map[string]any{
			"admin_username": &schemas.SecretVar{},
			"admin_password": &schemas.SecretVar{},
			"is_enabled":     false,
		}
	}
	mapConfig["is_db_connected"] = h.store.ConfigStore != nil
	if h.store.EnvLabel != "" {
		mapConfig["env_label"] = h.store.EnvLabel
	}
	mapConfig["is_git_available"] = CheckGitAvailability()
	mapConfig["is_cache_connected"] = h.store.VectorStore != nil
	mapConfig["is_logs_connected"] = h.store.LogsStore != nil
	mapConfig["is_object_storage_connected"] = h.store.LogsStoreConfig != nil && h.store.LogsStoreConfig.ObjectStorage != nil
	// Fetching proxy config
	if h.store.ConfigStore != nil {
		proxyConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
		if err != nil {
			logger.Warn("failed to get proxy config from store: %v", err)
		} else if proxyConfig != nil {
			// Redact password if present
			if proxyConfig.Password != "" {
				proxyConfig.Password = "<redacted>"
			}
			mapConfig["proxy_config"] = proxyConfig
		}
		// Fetching restart required config
		restartConfig, err := h.store.ConfigStore.GetRestartRequiredConfig(ctx)
		if err != nil {
			logger.Warn("failed to get restart required config from store: %v", err)
		} else if restartConfig != nil {
			mapConfig["restart_required"] = restartConfig
		}
		// Fetching UI/admin metadata blob (onboarding_dismissed, etc.).
		// This is a free-form key/value store that bypasses config.json sync.
		if metadata, err := h.store.ConfigStore.GetClientMetadata(ctx); err != nil {
			if !errors.Is(err, configstore.ErrNotFound) {
				logger.Warn("failed to get client metadata from store: %v", err)
			}
		} else if len(metadata) > 0 {
			mapConfig["metadata"] = metadata
		}
	}
	SendJSON(ctx, mapConfig)
}

// exportConfig handles GET /api/config/export - Get the complete exportable configuration conforming to https://www.getbifrost.ai/schema
func (h *ConfigHandler) exportConfig(ctx *fasthttp.RequestCtx) {
	exportMap := make(map[string]any)
	exportMap["$schema"] = "https://www.getbifrost.ai/schema"
	exportMap["version"] = 2
	exportMap["source_of_truth"] = "split"

	// Server config
	readBufferSize := 65536
	if h.store.ServerConfig != nil && h.store.ServerConfig.ReadBufferSize > 0 {
		readBufferSize = h.store.ServerConfig.ReadBufferSize
	}
	exportMap["server"] = map[string]any{
		"read_buffer_size": readBufferSize,
	}

	if h.store.EnvLabel != "" {
		exportMap["env_label"] = h.store.EnvLabel
	}

	// Client config
	var clientCfg *configstore.ClientConfig
	if h.store.ConfigStore != nil {
		if cc, err := h.store.ConfigStore.GetClientConfig(ctx); err == nil && cc != nil {
			clientCfg = cc
		}
	}
	if clientCfg == nil {
		clientCfg = h.store.ClientConfig
	}
	if clientCfg != nil {
		exportMap["client"] = formatExportClientConfig(clientCfg.Redacted())
	}

	// Providers
	var providersConfig map[schemas.ModelProvider]configstore.ProviderConfig
	if h.store.ConfigStore != nil {
		if pConfigs, err := h.store.ConfigStore.GetProvidersConfig(ctx); err == nil && len(pConfigs) > 0 {
			providersConfig = pConfigs
		}
	}
	if providersConfig == nil {
		h.store.Mu.RLock()
		if len(h.store.Providers) > 0 {
			providersConfig = make(map[schemas.ModelProvider]configstore.ProviderConfig, len(h.store.Providers))
			for k, v := range h.store.Providers {
				providersConfig[k] = v
			}
		}
		h.store.Mu.RUnlock()
	}
	if len(providersConfig) > 0 {
		formattedProviders := make(map[string]any)
		for pName, pCfg := range providersConfig {
			redacted := pCfg.Redacted()
			formattedProviders[string(pName)] = formatExportProviderConfig(redacted)
		}
		exportMap["providers"] = formattedProviders
	}

	// Framework
	var frameworkCfg any
	if h.store.ConfigStore != nil {
		if fc, err := h.store.ConfigStore.GetFrameworkConfig(ctx); err == nil && fc != nil {
			normalized, _, _ := lib.ResolveFrameworkPricingConfig(fc, nil)
			frameworkCfg = normalized
		}
	}
	if frameworkCfg == nil {
		h.store.Mu.RLock()
		storedFC := h.store.FrameworkConfig
		h.store.Mu.RUnlock()
		normalized, _, _ := lib.ResolveFrameworkPricingConfig(nil, storedFC)
		frameworkCfg = normalized
	}
	if frameworkCfg != nil {
		if fc, ok := frameworkCfg.(*configstoreTables.TableFrameworkConfig); ok && fc != nil {
			exportMap["framework"] = formatExportFrameworkConfig(fc)
		}
	}

	// MCP
	var mcpCfg *schemas.MCPConfig
	if h.store.ConfigStore != nil {
		if m, err := h.store.ConfigStore.GetMCPConfig(ctx); err == nil && m != nil {
			mcpCfg = m
		}
	}
	if mcpCfg == nil {
		mcpCfg = h.store.MCPConfig
	}
	if mcpCfg != nil && (len(mcpCfg.ClientConfigs) > 0 || mcpCfg.ToolManagerConfig != nil) {
		exportMap["mcp"] = formatExportMCPConfig(mcpCfg)
	}

	// Governance
	var govCfg *configstore.GovernanceConfig
	if h.store.ConfigStore != nil {
		if g, err := h.store.ConfigStore.GetGovernanceConfig(ctx); err == nil && g != nil {
			govCfg = g
		}
	}
	if govCfg == nil {
		govCfg = h.store.GovernanceConfig
	}
	if govCfg != nil {
		formattedGov := formatExportGovernanceConfig(govCfg)
		if len(formattedGov) > 0 {
			exportMap["governance"] = formattedGov
		}
	}

	// Plugins
	var pluginConfigs []*schemas.PluginConfig
	if h.store.ConfigStore != nil {
		if plugins, err := h.store.ConfigStore.GetPlugins(ctx); err == nil && len(plugins) > 0 {
			for _, p := range plugins {
				var cfg any
				if p.ConfigJSON != "" && p.ConfigJSON != "{}" {
					_ = json.Unmarshal([]byte(p.ConfigJSON), &cfg)
				}
				pluginConfigs = append(pluginConfigs, &schemas.PluginConfig{
					Name:      p.Name,
					Enabled:   p.Enabled,
					Path:      p.Path,
					Config:    cfg,
					Placement: p.Placement,
					Order:     p.Order,
				})
			}
		}
	}
	if len(pluginConfigs) == 0 && len(h.store.PluginConfigs) > 0 {
		pluginConfigs = h.store.PluginConfigs
	}
	if len(pluginConfigs) > 0 {
		exportMap["plugins"] = pluginConfigs
	}

	// Storage configs if configured
	if h.store.ConfigStore != nil {
		if vs, err := h.store.ConfigStore.GetVectorStoreConfig(ctx); err == nil && vs != nil {
			exportMap["vector_store"] = vs
		}
		if ls, err := h.store.ConfigStore.GetLogsStoreConfig(ctx); err == nil && ls != nil {
			exportMap["logs_store"] = ls
		}
	} else if h.store.LogsStoreConfig != nil {
		exportMap["logs_store"] = h.store.LogsStoreConfig
	}

	SendJSON(ctx, exportMap)
}

func formatExportClientConfig(c configstore.ClientConfig) map[string]any {
	out := make(map[string]any)

	// Fields with defaults (always include)
	out["drop_excess_requests"] = c.DropExcessRequests
	if c.InitialPoolSize > 0 {
		out["initial_pool_size"] = c.InitialPoolSize
	} else {
		out["initial_pool_size"] = 1000
	}

	if c.EnableLogging != nil {
		out["enable_logging"] = *c.EnableLogging
	} else {
		out["enable_logging"] = true
	}

	out["disable_content_logging"] = c.DisableContentLogging
	out["retain_content_in_object_storage"] = c.RetainContentInObjectStorage
	out["allow_per_request_content_storage_override"] = c.AllowPerRequestContentStorageOverride
	out["allow_per_request_raw_override"] = c.AllowPerRequestRawOverride
	out["allow_direct_keys"] = c.AllowDirectKeys
	out["disable_db_pings_in_health"] = c.DisableDBPingsInHealth
	out["dump_errors_in_console_logs"] = c.DumpErrorsInConsoleLogs

	if c.LogRetentionDays > 0 {
		out["log_retention_days"] = c.LogRetentionDays
	} else {
		out["log_retention_days"] = 365
	}

	if c.AsyncJobResultTTL > 0 {
		out["async_job_result_ttl"] = c.AsyncJobResultTTL
	} else {
		out["async_job_result_ttl"] = 3600
	}

	if c.RoutingChainMaxDepth > 0 {
		out["routing_chain_max_depth"] = c.RoutingChainMaxDepth
	} else {
		out["routing_chain_max_depth"] = 10
	}

	out["hide_deleted_virtual_keys_in_filters"] = c.HideDeletedVirtualKeysInFilters
	out["enforce_auth_on_inference"] = c.EnforceAuthOnInference

	out["compat"] = map[string]any{
		"convert_text_to_chat":      c.Compat.ConvertTextToChat,
		"convert_chat_to_responses": c.Compat.ConvertChatToResponses,
		"should_drop_params":        c.Compat.ShouldDropParams,
		"should_convert_params":     c.Compat.ShouldConvertParams,
	}

	// Optional fields with no default: include ONLY if non-empty
	if len(c.PrometheusLabels) > 0 {
		out["prometheus_labels"] = c.PrometheusLabels
	}
	if len(c.AllowedOrigins) > 0 {
		out["allowed_origins"] = c.AllowedOrigins
	}
	if len(c.AllowedHeaders) > 0 {
		out["allowed_headers"] = c.AllowedHeaders
	}
	if len(c.RequiredHeaders) > 0 {
		out["required_headers"] = c.RequiredHeaders
	}
	if len(c.LoggingHeaders) > 0 {
		out["logging_headers"] = c.LoggingHeaders
	}
	if len(c.RedactSensitiveHeaders) > 0 {
		out["redact_sensitive_headers"] = c.RedactSensitiveHeaders
	}
	if len(c.WhitelistedRoutes) > 0 {
		out["whitelisted_routes"] = c.WhitelistedRoutes
	}
	if c.MaxRequestBodySizeMB > 0 {
		out["max_request_body_size_mb"] = c.MaxRequestBodySizeMB
	}
	if c.DualCredentialConflictBehavior != "" {
		out["dual_credential_conflict_behavior"] = c.DualCredentialConflictBehavior
	}
	if c.HeaderFilterConfig != nil && (len(c.HeaderFilterConfig.Allowlist) > 0 || len(c.HeaderFilterConfig.Denylist) > 0) {
		out["header_filter_config"] = c.HeaderFilterConfig
	}
	if c.MCPExternalClientURL != nil && (c.MCPExternalClientURL.Val != "" || c.MCPExternalClientURL.GetRawRef() != "") {
		out["mcp_external_client_url"] = c.MCPExternalClientURL
	}
	if c.MCPServerAuthMode != "" {
		out["mcp_server_auth_mode"] = c.MCPServerAuthMode
	}
	if c.OAuth2ServerConfig != nil {
		out["oauth2_server_config"] = c.OAuth2ServerConfig
	}

	return out
}

func formatExportProviderConfig(p *configstore.ProviderConfig) map[string]any {
	if p == nil {
		return nil
	}
	out := make(map[string]any)

	if len(p.Keys) > 0 {
		keysList := make([]map[string]any, len(p.Keys))
		for i, k := range p.Keys {
			km := map[string]any{
				"name": k.Name,
			}
			if k.Value.IsFromSecret() && k.Value.GetRawRef() != "" {
				km["value"] = k.Value.GetRawRef()
			} else if k.Value.Val != "" {
				km["value"] = k.Value.Val
			}
			if len(k.Models) > 0 {
				km["models"] = k.Models
			}
			if len(k.BlacklistedModels) > 0 {
				km["blacklisted_models"] = k.BlacklistedModels
			}
			if k.Weight > 0 {
				km["weight"] = k.Weight
			} else {
				km["weight"] = 1
			}
			if k.Enabled != nil {
				km["enabled"] = *k.Enabled
			}
			if len(k.Aliases) > 0 {
				km["aliases"] = k.Aliases
			}
			if k.AzureKeyConfig != nil {
				km["azure_key_config"] = k.AzureKeyConfig
			}
			if k.VertexKeyConfig != nil {
				km["vertex_key_config"] = k.VertexKeyConfig
			}
			if k.BedrockKeyConfig != nil {
				km["bedrock_key_config"] = k.BedrockKeyConfig
			}
			if k.BedrockMantleKeyConfig != nil {
				km["bedrock_mantle_key_config"] = k.BedrockMantleKeyConfig
			}
			if k.OllamaKeyConfig != nil {
				km["ollama_key_config"] = k.OllamaKeyConfig
			}
			if k.VLLMKeyConfig != nil {
				km["vllm_key_config"] = k.VLLMKeyConfig
			}
			if k.ReplicateKeyConfig != nil {
				km["replicate_key_config"] = k.ReplicateKeyConfig
			}
			if k.SGLKeyConfig != nil {
				km["sgl_key_config"] = k.SGLKeyConfig
			}
			keysList[i] = km
		}
		out["keys"] = keysList
	} else {
		out["keys"] = []any{}
	}

	out["network_config"] = formatExportNetworkConfig(p.NetworkConfig)
	out["concurrency_and_buffer_size"] = formatExportConcurrencyAndBufferSize(p.ConcurrencyAndBufferSize)
	out["send_back_raw_request"] = p.SendBackRawRequest
	out["send_back_raw_response"] = p.SendBackRawResponse
	out["store_raw_request_response"] = p.StoreRawRequestResponse

	if p.ProxyConfig != nil {
		out["proxy_config"] = p.ProxyConfig
	}
	if p.CustomProviderConfig != nil {
		out["custom_provider_config"] = formatExportCustomProviderConfig(p.CustomProviderConfig)
	}
	if p.OpenAIConfig != nil {
		out["openai_config"] = p.OpenAIConfig
	}

	return out
}

func formatExportNetworkConfig(n *schemas.NetworkConfig) map[string]any {
	out := map[string]any{
		"default_request_timeout_in_seconds": schemas.DefaultRequestTimeoutInSeconds,
		"max_retries":                        0,
		"retry_backoff_initial":              500,
		"retry_backoff_max":                  5000,
		"allow_private_network":              false,
	}
	if n != nil {
		if n.BaseURL != "" {
			out["base_url"] = n.BaseURL
		}
		if len(n.ExtraHeaders) > 0 {
			out["extra_headers"] = n.ExtraHeaders
		}
		if n.DefaultRequestTimeoutInSeconds > 0 {
			out["default_request_timeout_in_seconds"] = n.DefaultRequestTimeoutInSeconds
		}
		if n.MaxRetries > 0 {
			out["max_retries"] = n.MaxRetries
		}
		if n.RetryBackoffInitial > 0 {
			out["retry_backoff_initial"] = int64(n.RetryBackoffInitial / time.Millisecond)
		}
		if n.RetryBackoffMax > 0 {
			out["retry_backoff_max"] = int64(n.RetryBackoffMax / time.Millisecond)
		}
		if n.EnforceHTTP2 {
			out["enforce_http2"] = n.EnforceHTTP2
		}
		if n.HTTP2PingIntervalInSeconds > 0 {
			out["http2_ping_interval_in_seconds"] = n.HTTP2PingIntervalInSeconds
		}
		if n.InsecureSkipVerify {
			out["insecure_skip_verify"] = n.InsecureSkipVerify
		}
		if n.CACertPEM != nil {
			if n.CACertPEM.IsFromSecret() && n.CACertPEM.GetRawRef() != "" {
				out["ca_cert_pem"] = n.CACertPEM.GetRawRef()
			} else if n.CACertPEM.Val != "" {
				out["ca_cert_pem"] = n.CACertPEM.Val
			}
		}
		if n.StreamIdleTimeoutInSeconds > 0 {
			out["stream_idle_timeout_in_seconds"] = n.StreamIdleTimeoutInSeconds
		}
		if n.KeepAliveTimeoutInSeconds > 0 {
			out["keep_alive_timeout_in_seconds"] = n.KeepAliveTimeoutInSeconds
		}
		if n.MaxConnsPerHost > 0 {
			out["max_conns_per_host"] = n.MaxConnsPerHost
		}
		if len(n.BetaHeaderOverrides) > 0 {
			out["beta_header_overrides"] = n.BetaHeaderOverrides
		}
		out["allow_private_network"] = n.AllowPrivateNetwork
	}
	return out
}

func formatExportConcurrencyAndBufferSize(c *schemas.ConcurrencyAndBufferSize) map[string]any {
	concurrency := schemas.DefaultConcurrency
	bufferSize := schemas.DefaultBufferSize
	if c != nil {
		if c.Concurrency > 0 {
			concurrency = c.Concurrency
		}
		if c.BufferSize > 0 {
			bufferSize = c.BufferSize
		}
	}
	return map[string]any{
		"concurrency": concurrency,
		"buffer_size": bufferSize,
	}
}

func formatExportFrameworkConfig(fc *configstoreTables.TableFrameworkConfig) map[string]any {
	if fc == nil {
		return nil
	}
	pricing := make(map[string]any)
	if fc.PricingURL != nil && *fc.PricingURL != "" {
		pricing["pricing_url"] = *fc.PricingURL
	}
	if fc.PricingSyncInterval != nil && *fc.PricingSyncInterval > 0 {
		pricing["pricing_sync_interval"] = *fc.PricingSyncInterval
	} else {
		pricing["pricing_sync_interval"] = 86400
	}
	if fc.ModelParametersURL != nil && *fc.ModelParametersURL != "" {
		pricing["model_parameters_url"] = *fc.ModelParametersURL
	}
	if fc.MCPLibraryURL != nil && *fc.MCPLibraryURL != "" {
		pricing["mcp_library_url"] = *fc.MCPLibraryURL
	}
	if fc.MCPLibrarySyncInterval != nil && *fc.MCPLibrarySyncInterval > 0 {
		pricing["mcp_library_sync_interval"] = *fc.MCPLibrarySyncInterval
	} else {
		pricing["mcp_library_sync_interval"] = 86400
	}
	if fc.LiveModelsSyncInterval != nil && *fc.LiveModelsSyncInterval > 0 {
		pricing["live_models_sync_interval"] = *fc.LiveModelsSyncInterval
	} else {
		pricing["live_models_sync_interval"] = 86400
	}

	return map[string]any{
		"pricing": pricing,
	}
}

func formatExportCustomProviderConfig(c *schemas.CustomProviderConfig) map[string]any {
	if c == nil {
		return nil
	}
	out := map[string]any{
		"base_provider_type": c.BaseProviderType,
		"is_key_less":        c.IsKeyLess,
		"allowed_requests":   formatExportAllowedRequests(c.AllowedRequests),
	}
	if len(c.RequestPathOverrides) > 0 {
		out["request_path_overrides"] = c.RequestPathOverrides
	}
	return out
}

func formatExportAllowedRequests(ar *schemas.AllowedRequests) map[string]bool {
	if ar == nil {
		return map[string]bool{
			"list_models":             true,
			"text_completion":         true,
			"text_completion_stream":  true,
			"chat_completion":         true,
			"chat_completion_stream":  true,
			"responses":               true,
			"responses_stream":        true,
			"responses_retrieve":      true,
			"responses_delete":        true,
			"responses_cancel":        true,
			"responses_input_items":   true,
			"count_tokens":            true,
			"compaction":              true,
			"embedding":               true,
			"rerank":                  true,
			"ocr":                     true,
			"speech":                  true,
			"speech_stream":           true,
			"transcription":           true,
			"transcription_stream":    true,
			"image_generation":        true,
			"image_generation_stream": true,
			"image_edit":              true,
			"image_edit_stream":       true,
			"image_variation":         true,
			"video_generation":        true,
			"video_retrieve":          true,
			"video_download":          true,
			"video_delete":            true,
			"video_list":              true,
			"video_remix":             true,
			"batch_create":            true,
			"batch_list":              true,
			"batch_retrieve":          true,
			"batch_cancel":            true,
			"batch_delete":            true,
			"batch_results":           true,
			"file_upload":             true,
			"file_list":               true,
			"file_retrieve":           true,
			"file_delete":             true,
			"file_content":            true,
			"container_create":        true,
			"container_list":          true,
			"container_retrieve":      true,
			"container_delete":        true,
			"container_file_create":   true,
			"container_file_list":     true,
			"container_file_retrieve": true,
			"container_file_content":  true,
			"container_file_delete":   true,
			"passthrough":             true,
			"passthrough_stream":      true,
			"websocket_responses":     true,
			"realtime":                false,
			"cached_content_create":   true,
			"cached_content_list":     true,
			"cached_content_retrieve": true,
			"cached_content_update":   true,
			"cached_content_delete":   true,
		}
	}

	return map[string]bool{
		"list_models":             ar.ListModels,
		"text_completion":         ar.TextCompletion,
		"text_completion_stream":  ar.TextCompletionStream,
		"chat_completion":         ar.ChatCompletion,
		"chat_completion_stream":  ar.ChatCompletionStream,
		"responses":               ar.Responses,
		"responses_stream":        ar.ResponsesStream,
		"responses_retrieve":      ar.ResponsesRetrieve,
		"responses_delete":        ar.ResponsesDelete,
		"responses_cancel":        ar.ResponsesCancel,
		"responses_input_items":   ar.ResponsesInputItems,
		"count_tokens":            ar.CountTokens,
		"compaction":              ar.Compaction,
		"embedding":               ar.Embedding,
		"rerank":                  ar.Rerank,
		"ocr":                     ar.OCR,
		"speech":                  ar.Speech,
		"speech_stream":           ar.SpeechStream,
		"transcription":           ar.Transcription,
		"transcription_stream":    ar.TranscriptionStream,
		"image_generation":        ar.ImageGeneration,
		"image_generation_stream": ar.ImageGenerationStream,
		"image_edit":              ar.ImageEdit,
		"image_edit_stream":       ar.ImageEditStream,
		"image_variation":         ar.ImageVariation,
		"video_generation":        ar.VideoGeneration,
		"video_retrieve":          ar.VideoRetrieve,
		"video_download":          ar.VideoDownload,
		"video_delete":            ar.VideoDelete,
		"video_list":              ar.VideoList,
		"video_remix":             ar.VideoRemix,
		"batch_create":            ar.BatchCreate,
		"batch_list":              ar.BatchList,
		"batch_retrieve":          ar.BatchRetrieve,
		"batch_cancel":            ar.BatchCancel,
		"batch_delete":            ar.BatchDelete,
		"batch_results":           ar.BatchResults,
		"file_upload":             ar.FileUpload,
		"file_list":               ar.FileList,
		"file_retrieve":           ar.FileRetrieve,
		"file_delete":             ar.FileDelete,
		"file_content":            ar.FileContent,
		"container_create":        ar.ContainerCreate,
		"container_list":          ar.ContainerList,
		"container_retrieve":      ar.ContainerRetrieve,
		"container_delete":        ar.ContainerDelete,
		"container_file_create":   ar.ContainerFileCreate,
		"container_file_list":     ar.ContainerFileList,
		"container_file_retrieve": ar.ContainerFileRetrieve,
		"container_file_content":  ar.ContainerFileContent,
		"container_file_delete":   ar.ContainerFileDelete,
		"passthrough":             ar.Passthrough,
		"passthrough_stream":      ar.PassthroughStream,
		"websocket_responses":     ar.WebSocketResponses,
		"realtime":                ar.Realtime,
		"cached_content_create":   ar.CachedContentCreate,
		"cached_content_list":     ar.CachedContentList,
		"cached_content_retrieve": ar.CachedContentRetrieve,
		"cached_content_update":   ar.CachedContentUpdate,
		"cached_content_delete":   ar.CachedContentDelete,
	}
}

func formatExportMCPConfig(m *schemas.MCPConfig) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any)
	if len(m.ClientConfigs) > 0 {
		out["client_configs"] = m.ClientConfigs
	}
	if m.ToolManagerConfig != nil {
		out["tool_manager_config"] = m.ToolManagerConfig
	}
	if m.ToolSyncInterval > 0 {
		out["tool_sync_interval"] = m.ToolSyncInterval
	}
	return out
}

func formatExportGovernanceConfig(g *configstore.GovernanceConfig) map[string]any {
	if g == nil {
		return nil
	}
	out := make(map[string]any)
	if g.AuthConfig != nil {
		out["auth_config"] = g.AuthConfig
	}
	if len(g.VirtualKeys) > 0 {
		out["virtual_keys"] = g.VirtualKeys
	}
	if len(g.Teams) > 0 {
		out["teams"] = g.Teams
	}
	if len(g.Customers) > 0 {
		out["customers"] = g.Customers
	}
	if len(g.Budgets) > 0 {
		out["budgets"] = g.Budgets
	}
	if len(g.RateLimits) > 0 {
		out["rate_limits"] = g.RateLimits
	}
	if len(g.RoutingRules) > 0 {
		out["routing_rules"] = g.RoutingRules
	}
	if g.ComplexityAnalyzerConfig != nil {
		out["complexity_analyzer_config"] = g.ComplexityAnalyzerConfig
	}
	return out
}


// updateMetadata handles POST /api/config/metadata - merges a JSON object of
// key/value pairs into the ClientConfig metadata blob. Keys with a nil value
// are removed. Intended for UI/admin preferences (onboarding state, dismissed
// tooltips, etc.) and is auth-gated by the same middleware as the rest of /api/config.
func (h *ConfigHandler) updateMetadata(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}
	var patch map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &patch); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	if len(patch) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "patch body must contain at least one key")
		return
	}
	if err := h.store.ConfigStore.UpdateClientMetadata(ctx, patch); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("failed to update metadata: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update metadata: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{"success": true})
}

// updateConfig updates the core configuration settings.
// Currently, it supports hot-reloading of the `drop_excess_requests` setting.
// Note that settings like `prometheus_labels` cannot be changed at runtime.
func (h *ConfigHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Config store not initialized")
		return
	}

	payload := struct {
		ClientConfig    configstore.ClientConfig               `json:"client_config"`
		FrameworkConfig configstoreTables.TableFrameworkConfig `json:"framework_config"`
		AuthConfig      *configstore.AuthConfig                `json:"auth_config"`
	}{}

	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	// Validate MCP external URL overrides up front — the rest of this handler
	// applies live mutations (drop-excess flag, MCP tool-manager reload, compat
	// plugin reload, in-memory MCP config) before persisting, so a late
	// rejection would leave the process in a partially-updated state.
	if err := lib.ValidateBaseURL(payload.ClientConfig.MCPExternalClientURL.GetValue()); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("mcp_external_client_url %v", err))
		return
	}

	// Validating framework config
	if payload.FrameworkConfig.PricingURL != nil && *payload.FrameworkConfig.PricingURL != modelcatalog.DefaultPricingURL {
		if err := checkURLAccessibility(*payload.FrameworkConfig.PricingURL); err != nil {
			logger.Warn("failed to check the accessibility of the pricing URL: %v", err)
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err))
			return
		}
	}
	if payload.FrameworkConfig.ModelParametersURL != nil && *payload.FrameworkConfig.ModelParametersURL != "" && *payload.FrameworkConfig.ModelParametersURL != modelcatalog.DefaultModelParametersURL {
		if err := checkURLAccessibility(*payload.FrameworkConfig.ModelParametersURL); err != nil {
			logger.Warn("failed to check the accessibility of the model parameters URL: %v", err)
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to check the accessibility of the model parameters URL: %v", err))
			return
		}
	}

	// Checking the pricing sync interval
	if payload.FrameworkConfig.PricingSyncInterval != nil && *payload.FrameworkConfig.PricingSyncInterval <= 0 {
		logger.Warn("pricing sync interval must be greater than 0")
		SendError(ctx, fasthttp.StatusBadRequest, "pricing sync interval must be greater than 0")
		return
	}

	// Validate MCP library catalog URL override (only when set and non-default)
	if payload.FrameworkConfig.MCPLibraryURL != nil && *payload.FrameworkConfig.MCPLibraryURL != "" && *payload.FrameworkConfig.MCPLibraryURL != modelcatalog.DefaultMCPLibraryURL {
		if err := checkURLAccessibility(*payload.FrameworkConfig.MCPLibraryURL); err != nil {
			logger.Warn("failed to check the accessibility of the MCP library URL: %v", err)
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to check the accessibility of the MCP library URL: %v", err))
			return
		}
	}
	// Checking the MCP library sync interval
	if payload.FrameworkConfig.MCPLibrarySyncInterval != nil && *payload.FrameworkConfig.MCPLibrarySyncInterval <= 0 {
		logger.Warn("MCP library sync interval must be greater than 0")
		SendError(ctx, fasthttp.StatusBadRequest, "MCP library sync interval must be greater than 0")
		return
	}
	// Checking the live models sync interval. Unlike the intervals above, 0 is
	// accepted: it is the documented way to turn the background refresher off.
	if payload.FrameworkConfig.LiveModelsSyncInterval != nil {
		interval := *payload.FrameworkConfig.LiveModelsSyncInterval
		if interval < 0 {
			logger.Warn("live models sync interval cannot be negative")
			SendError(ctx, fasthttp.StatusBadRequest, "live models sync interval cannot be negative (use 0 to disable background refresh)")
			return
		}
		if interval > 0 && interval < modelcatalog.MinimumLiveModelsSyncIntervalSec {
			logger.Warn("live models sync interval is below the minimum of %d seconds", modelcatalog.MinimumLiveModelsSyncIntervalSec)
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("live models sync interval must be 0 (disabled) or at least %d seconds", modelcatalog.MinimumLiveModelsSyncIntervalSec))
			return
		}
	}

	// Get current config with proper locking
	currentConfig := h.store.ClientConfig
	updatedConfig := currentConfig

	// Validate MCP auth-mode / OAuth2 server settings before any live mutation
	// below (drop-excess flag, MCP tool-manager reload, compat plugin reload,
	// in-memory MCP config). A late rejection would return 400 while runtime
	// state had already changed but DB persistence was skipped, diverging
	// in-memory, core, and DB state.

	// Validate the inbound MCP auth mode against the allowed enum
	// (config.schema.json is the source of truth: headers | both | oauth).
	switch payload.ClientConfig.MCPServerAuthMode {
	case "", configstoreTables.MCPServerAuthModeHeaders, configstoreTables.MCPServerAuthModeBoth, configstoreTables.MCPServerAuthModeOAuth:
		// valid; empty means the field was omitted from a partial update
	default:
		SendError(ctx, fasthttp.StatusBadRequest, "mcp_server_auth_mode must be one of: headers, both, oauth")
		return
	}

	// oauth2_server_config only applies when discovery is enabled (both | oauth).
	// Evaluate against the effective mode so a partial update that supplies only
	// the config cannot smuggle it in while the stored mode is headers.
	effectiveAuthMode := payload.ClientConfig.MCPServerAuthMode
	if effectiveAuthMode == "" {
		effectiveAuthMode = currentConfig.MCPServerAuthMode
	}
	effectiveOAuth2Config := currentConfig.OAuth2ServerConfig
	if payload.ClientConfig.OAuth2ServerConfig != nil {
		effectiveOAuth2Config = payload.ClientConfig.OAuth2ServerConfig
	}

	// disable_vk_identity only makes sense in oauth mode: in both mode virtual
	// keys can still authenticate via headers, so suppressing them in the consent
	// flow alone would be misleading. Evaluate the merged config so a partial
	// update that switches the mode away from oauth (without resending the config)
	// cannot leave a previously stored disable_vk_identity active.
	if effectiveOAuth2Config != nil &&
		effectiveOAuth2Config.DisableVKIdentity &&
		effectiveAuthMode != configstoreTables.MCPServerAuthModeOAuth {
		SendError(ctx, fasthttp.StatusBadRequest, "disable_vk_identity is only valid when mcp_server_auth_mode is oauth")
		return
	}

	// Cap auth_code_ttl so a leaked one-time code can't stay valid for long.
	// This is an unconditional invariant on the stored value — enforced in every
	// mode (not just both | oauth), mirroring the load-time validateClientConfig
	// check — so a save can never persist a value that would then fail boot on the
	// next restart. A zero/omitted value falls back to the default at issuance and
	// is left alone here.
	if effectiveOAuth2Config != nil &&
		effectiveOAuth2Config.AuthCodeTTL > configstoreTables.MaxAuthCodeTTL {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("auth_code_ttl must not exceed %d seconds (15 minutes)", configstoreTables.MaxAuthCodeTTL))
		return
	}

	var restartReasons []string

	if payload.ClientConfig.DropExcessRequests != currentConfig.DropExcessRequests {
		h.configManager.UpdateDropExcessRequests(ctx, payload.ClientConfig.DropExcessRequests)
		updatedConfig.DropExcessRequests = payload.ClientConfig.DropExcessRequests
	}

	if payload.ClientConfig.MCPCodeModeBindingLevel != "" {
		if payload.ClientConfig.MCPCodeModeBindingLevel != string(schemas.CodeModeBindingLevelServer) && payload.ClientConfig.MCPCodeModeBindingLevel != string(schemas.CodeModeBindingLevelTool) {
			logger.Warn("mcp_code_mode_binding_level must be 'server' or 'tool'")
			SendError(ctx, fasthttp.StatusBadRequest, "mcp_code_mode_binding_level must be 'server' or 'tool'")
			return
		}
	}

	shouldReloadMCPToolManagerConfig := false

	// Only process MCPAgentDepth if explicitly provided (> 0) and different from current
	if payload.ClientConfig.MCPAgentDepth > 0 && payload.ClientConfig.MCPAgentDepth != currentConfig.MCPAgentDepth {
		updatedConfig.MCPAgentDepth = payload.ClientConfig.MCPAgentDepth
		shouldReloadMCPToolManagerConfig = true
	}

	// Only process MCPToolExecutionTimeout if explicitly provided (> 0) and different from current
	if payload.ClientConfig.MCPToolExecutionTimeout > 0 && payload.ClientConfig.MCPToolExecutionTimeout != currentConfig.MCPToolExecutionTimeout {
		updatedConfig.MCPToolExecutionTimeout = payload.ClientConfig.MCPToolExecutionTimeout
		shouldReloadMCPToolManagerConfig = true
	}

	if payload.ClientConfig.MCPCodeModeBindingLevel != "" && payload.ClientConfig.MCPCodeModeBindingLevel != currentConfig.MCPCodeModeBindingLevel {
		updatedConfig.MCPCodeModeBindingLevel = payload.ClientConfig.MCPCodeModeBindingLevel
		shouldReloadMCPToolManagerConfig = true
	}

	if payload.ClientConfig.MCPDisableAutoToolInject != currentConfig.MCPDisableAutoToolInject {
		updatedConfig.MCPDisableAutoToolInject = payload.ClientConfig.MCPDisableAutoToolInject
		shouldReloadMCPToolManagerConfig = true
	}
	// MCPToolSyncInterval supports 0 (disabled), so compare against current value
	// instead of a > 0 guard used by other numeric fields.
	if payload.ClientConfig.MCPToolSyncInterval != currentConfig.MCPToolSyncInterval {
		updatedConfig.MCPToolSyncInterval = payload.ClientConfig.MCPToolSyncInterval
	}
	updatedConfig.MCPEnableTempTokenAuth = payload.ClientConfig.MCPEnableTempTokenAuth

	// Reload MCP tool manager config with all current values in one call
	if shouldReloadMCPToolManagerConfig && h.store.MCPConfig != nil {
		if err := h.configManager.UpdateMCPToolManagerConfig(ctx, updatedConfig.MCPAgentDepth, updatedConfig.MCPToolExecutionTimeout, updatedConfig.MCPCodeModeBindingLevel, updatedConfig.MCPDisableAutoToolInject); err != nil {
			logger.Warn(fmt.Sprintf("failed to update mcp tool manager config: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update mcp tool manager config: %v", err))
			return
		}
	}
	// Keep in-memory MCP config aligned with client-config-backed MCP settings.
	if h.store.MCPConfig != nil {
		if h.store.MCPConfig.ToolManagerConfig == nil {
			h.store.MCPConfig.ToolManagerConfig = &schemas.MCPToolManagerConfig{}
		}
		h.store.MCPConfig.ToolSyncInterval = time.Duration(updatedConfig.MCPToolSyncInterval) * time.Second
		h.store.MCPConfig.ToolManagerConfig.MaxAgentDepth = updatedConfig.MCPAgentDepth
		h.store.MCPConfig.ToolManagerConfig.ToolExecutionTimeout = schemas.Duration(time.Duration(updatedConfig.MCPToolExecutionTimeout) * time.Second)
		h.store.MCPConfig.ToolManagerConfig.CodeModeBindingLevel = schemas.CodeModeBindingLevel(updatedConfig.MCPCodeModeBindingLevel)
		h.store.MCPConfig.ToolManagerConfig.DisableAutoToolInject = updatedConfig.MCPDisableAutoToolInject
	}

	if !slices.Equal(payload.ClientConfig.PrometheusLabels, currentConfig.PrometheusLabels) {
		updatedConfig.PrometheusLabels = payload.ClientConfig.PrometheusLabels
		restartReasons = append(restartReasons, "Prometheus labels")
	}

	if !slices.Equal(payload.ClientConfig.AllowedOrigins, currentConfig.AllowedOrigins) {
		updatedConfig.AllowedOrigins = payload.ClientConfig.AllowedOrigins
		restartReasons = append(restartReasons, "Allowed origins")
	}

	if !slices.Equal(payload.ClientConfig.AllowedHeaders, currentConfig.AllowedHeaders) {
		updatedConfig.AllowedHeaders = payload.ClientConfig.AllowedHeaders
		restartReasons = append(restartReasons, "Allowed headers")
	}

	// Only update InitialPoolSize if explicitly provided (> 0) to avoid clearing stored value
	if payload.ClientConfig.InitialPoolSize > 0 {
		if payload.ClientConfig.InitialPoolSize != currentConfig.InitialPoolSize {
			restartReasons = append(restartReasons, "Initial pool size")
		}
		updatedConfig.InitialPoolSize = payload.ClientConfig.InitialPoolSize
	}

	if payload.ClientConfig.EnableLogging != nil {
		payloadLogging := *payload.ClientConfig.EnableLogging
		currentLogging := currentConfig.EnableLogging == nil || *currentConfig.EnableLogging
		if payloadLogging != currentLogging {
			restartReasons = append(restartReasons, "Logging changed")
		}
		updatedConfig.EnableLogging = payload.ClientConfig.EnableLogging
	}

	// No restart needed - logging plugin holds a live pointer to ClientConfig.DisableContentLogging,
	// and ReloadClientConfigFromConfigStore mutates the struct in place so the next request picks up the new value.
	updatedConfig.DisableContentLogging = payload.ClientConfig.DisableContentLogging
	// No restart needed - logging plugin holds a live pointer to ClientConfig.RetainContentInObjectStorage.
	updatedConfig.RetainContentInObjectStorage = payload.ClientConfig.RetainContentInObjectStorage
	updatedConfig.DisableDBPingsInHealth = payload.ClientConfig.DisableDBPingsInHealth
	// No restart needed - ReloadClientConfigFromConfigStore calls CorsMiddleware.UpdateConfig,
	// which atomically swaps in a fresh immutable snapshot carrying the new value.
	updatedConfig.DumpErrorsInConsoleLogs = payload.ClientConfig.DumpErrorsInConsoleLogs

	updatedConfig.EnforceAuthOnInference = payload.ClientConfig.EnforceAuthOnInference
	// Sync deprecated columns to match new field so they stay consistent in the DB
	updatedConfig.EnforceGovernanceHeader = payload.ClientConfig.EnforceAuthOnInference
	updatedConfig.EnforceSCIMAuth = payload.ClientConfig.EnforceAuthOnInference

	// Only update when explicitly provided to avoid clearing the stored default (prefer_idp)
	if payload.ClientConfig.DualCredentialConflictBehavior != "" {
		updatedConfig.DualCredentialConflictBehavior = payload.ClientConfig.DualCredentialConflictBehavior
	}

	// Only update MaxRequestBodySizeMB if explicitly provided (> 0) to avoid clearing stored value
	if payload.ClientConfig.MaxRequestBodySizeMB > 0 {
		if payload.ClientConfig.MaxRequestBodySizeMB != currentConfig.MaxRequestBodySizeMB {
			restartReasons = append(restartReasons, "Max request body size")
		}
		updatedConfig.MaxRequestBodySizeMB = payload.ClientConfig.MaxRequestBodySizeMB
	}

	// Handle compat plugin toggle
	newCompat := payload.ClientConfig.Compat
	oldCompat := currentConfig.Compat
	if newCompat != oldCompat {
		newEnabled := newCompat.ConvertTextToChat || newCompat.ConvertChatToResponses || newCompat.ShouldDropParams || newCompat.ShouldConvertParams
		if newEnabled {
			compatCfg := &compat.Config{
				ConvertTextToChat:      newCompat.ConvertTextToChat,
				ConvertChatToResponses: newCompat.ConvertChatToResponses,
				ShouldDropParams:       newCompat.ShouldDropParams,
				ShouldConvertParams:    newCompat.ShouldConvertParams,
			}
			if err := h.configManager.ReloadPlugin(ctx, compat.PluginName, nil, compatCfg, nil, nil); err != nil {
				logger.Warn("failed to load compat plugin: %v", err)
				SendError(ctx, 400, "Failed to load compat plugin")
				return
			}
		} else {
			disabledCtx := context.WithValue(ctx, PluginDisabledKey, true)
			if err := h.configManager.RemovePlugin(disabledCtx, compat.PluginName); err != nil {
				logger.Warn("failed to remove compat plugin: %v", err)
				SendError(ctx, 400, "Failed to remove compat plugin")
				return
			}
		}
	}
	updatedConfig.Compat = newCompat
	// Only update MCP fields if explicitly provided (non-zero) to avoid clearing stored values
	if payload.ClientConfig.MCPAgentDepth > 0 {
		updatedConfig.MCPAgentDepth = payload.ClientConfig.MCPAgentDepth
	}
	if payload.ClientConfig.MCPToolExecutionTimeout > 0 {
		updatedConfig.MCPToolExecutionTimeout = payload.ClientConfig.MCPToolExecutionTimeout
	}
	// 0 is a valid value (disabled), so persist it when changed.
	if payload.ClientConfig.MCPToolSyncInterval != currentConfig.MCPToolSyncInterval {
		updatedConfig.MCPToolSyncInterval = payload.ClientConfig.MCPToolSyncInterval
	}
	// Only update MCPCodeModeBindingLevel if payload is non-empty to avoid clearing stored value
	if payload.ClientConfig.MCPCodeModeBindingLevel != "" {
		updatedConfig.MCPCodeModeBindingLevel = payload.ClientConfig.MCPCodeModeBindingLevel
	}

	// Only update AsyncJobResultTTL if explicitly provided (> 0) to avoid clearing stored value
	if payload.ClientConfig.AsyncJobResultTTL > 0 {
		updatedConfig.AsyncJobResultTTL = payload.ClientConfig.AsyncJobResultTTL
	}

	// Handle RequiredHeaders changes (no restart needed - governance plugin reads via pointer)
	updatedConfig.RequiredHeaders = payload.ClientConfig.RequiredHeaders

	// Handle LoggingHeaders changes (no restart needed - logging plugin reads via pointer)
	updatedConfig.LoggingHeaders = payload.ClientConfig.LoggingHeaders
	updatedConfig.RedactSensitiveHeaders = payload.ClientConfig.RedactSensitiveHeaders

	// Handle WhitelistedRoutes changes (updated dynamically via AuthMiddleware)
	updatedConfig.WhitelistedRoutes = payload.ClientConfig.WhitelistedRoutes

	// Toggle whether deleted virtual keys should appear in logs filter data.
	updatedConfig.HideDeletedVirtualKeysInFilters = payload.ClientConfig.HideDeletedVirtualKeysInFilters

	// Toggle allowing per-request override for content storage and raw request/response storage
	updatedConfig.AllowPerRequestContentStorageOverride = payload.ClientConfig.AllowPerRequestContentStorageOverride

	// Toggle allowing per-request override for raw request/response exposure
	updatedConfig.AllowPerRequestRawOverride = payload.ClientConfig.AllowPerRequestRawOverride

	// Toggle allowing direct key bypass via x-bf-direct-key header
	updatedConfig.AllowDirectKeys = payload.ClientConfig.AllowDirectKeys

	// No restart needed - routing engine reads via pointer, change is effective immediately.
	if payload.ClientConfig.RoutingChainMaxDepth > 0 {
		updatedConfig.RoutingChainMaxDepth = payload.ClientConfig.RoutingChainMaxDepth
	}

	// Update external base URL for OAuth client redirect_uri (nil clears the override).
	// Validation is performed up front in this handler so a failure here cannot leave the process in a partial state.
	updatedConfig.MCPExternalClientURL = payload.ClientConfig.MCPExternalClientURL

	// Only update each field when explicitly provided so partial /api/config
	// payloads do not clear stored values (matches the MCP field handling above).
	// The enum, disable_vk_identity, and auth_code_ttl validations for these
	// fields run up front (before any live mutation) so a rejection can't leave
	// runtime and DB state diverged.
	if payload.ClientConfig.MCPServerAuthMode != "" {
		updatedConfig.MCPServerAuthMode = payload.ClientConfig.MCPServerAuthMode
	}
	if payload.ClientConfig.OAuth2ServerConfig != nil {
		updatedConfig.OAuth2ServerConfig = payload.ClientConfig.OAuth2ServerConfig
	}

	// Handle HeaderFilterConfig changes
	if !headerFilterConfigEqual(payload.ClientConfig.HeaderFilterConfig, currentConfig.HeaderFilterConfig) {
		// Validate that no security headers are in the allowlist or denylist
		if err := validateHeaderFilterConfig(payload.ClientConfig.HeaderFilterConfig); err != nil {
			logger.Warn("invalid header filter config: %v", err)
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		updatedConfig.HeaderFilterConfig = payload.ClientConfig.HeaderFilterConfig
		if err := h.configManager.ReloadHeaderFilterConfig(ctx, payload.ClientConfig.HeaderFilterConfig); err != nil {
			logger.Warn("failed to reload header filter config: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload header filter config: %v", err))
			return
		}
	}

	// Validate LogRetentionDays
	if payload.ClientConfig.LogRetentionDays < 1 {
		logger.Warn("log_retention_days must be at least 1")
		SendError(ctx, fasthttp.StatusBadRequest, "log_retention_days must be at least 1")
		return
	}
	updatedConfig.LogRetentionDays = payload.ClientConfig.LogRetentionDays

	if err := h.store.ConfigStore.UpdateClientConfig(ctx, updatedConfig); err != nil {
		logger.Warn("failed to save configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save configuration: %v", err))
		return
	}

	// Apply the in-memory change only after persistence succeeds.
	h.store.ClientConfig = updatedConfig
	// Reloading client config from config store
	if err := h.configManager.ReloadClientConfigFromConfigStore(ctx); err != nil {
		logger.Warn("failed to reload client config from config store: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload client config from config store: %v", err))
		return
	}
	// Fetching existing framework config
	frameworkConfig, err := h.store.ConfigStore.GetFrameworkConfig(ctx)
	if err != nil {
		logger.Warn("failed to get framework config from store: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get framework config from store: %v", err))
		return
	}
	// if framework config is nil, we will use the default pricing config
	if frameworkConfig == nil {
		frameworkConfig = &configstoreTables.TableFrameworkConfig{
			ID:                     0,
			PricingURL:             bifrost.Ptr(modelcatalog.DefaultPricingURL),
			PricingSyncInterval:    bifrost.Ptr(int64(modelcatalog.DefaultSyncInterval.Seconds())),
			ModelParametersURL:     bifrost.Ptr(modelcatalog.DefaultModelParametersURL),
			MCPLibraryURL:          bifrost.Ptr(modelcatalog.DefaultMCPLibraryURL),
			MCPLibrarySyncInterval: bifrost.Ptr(int64(modelcatalog.DefaultSyncInterval.Seconds())),
			LiveModelsSyncInterval: bifrost.Ptr(int64(modelcatalog.DefaultLiveModelsSyncInterval.Seconds())),
		}
	}
	// Handling individual nil cases
	if frameworkConfig.PricingURL == nil {
		frameworkConfig.PricingURL = bifrost.Ptr(modelcatalog.DefaultPricingURL)
	}
	if frameworkConfig.PricingSyncInterval == nil {
		frameworkConfig.PricingSyncInterval = bifrost.Ptr(int64(modelcatalog.DefaultSyncInterval.Seconds()))
	}
	if frameworkConfig.ModelParametersURL == nil {
		frameworkConfig.ModelParametersURL = bifrost.Ptr(modelcatalog.DefaultModelParametersURL)
	}
	if frameworkConfig.MCPLibraryURL == nil {
		frameworkConfig.MCPLibraryURL = bifrost.Ptr(modelcatalog.DefaultMCPLibraryURL)
	}
	if frameworkConfig.MCPLibrarySyncInterval == nil {
		frameworkConfig.MCPLibrarySyncInterval = bifrost.Ptr(int64(modelcatalog.DefaultSyncInterval.Seconds()))
	}
	if frameworkConfig.LiveModelsSyncInterval == nil {
		frameworkConfig.LiveModelsSyncInterval = bifrost.Ptr(int64(modelcatalog.DefaultLiveModelsSyncInterval.Seconds()))
	}
	// Updating framework config
	shouldReloadFrameworkConfig := false
	if payload.FrameworkConfig.PricingURL != nil && *payload.FrameworkConfig.PricingURL != *frameworkConfig.PricingURL {
		if err := checkURLAccessibility(*payload.FrameworkConfig.PricingURL); err != nil {
			logger.Warn("failed to check the accessibility of the pricing URL: %v", err)
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err))
			return
		}
		frameworkConfig.PricingURL = payload.FrameworkConfig.PricingURL
		shouldReloadFrameworkConfig = true
	}
	if payload.FrameworkConfig.PricingSyncInterval != nil {
		syncInterval := int64(*payload.FrameworkConfig.PricingSyncInterval)
		if syncInterval != *frameworkConfig.PricingSyncInterval {
			frameworkConfig.PricingSyncInterval = &syncInterval
			shouldReloadFrameworkConfig = true
		}
	}
	if payload.FrameworkConfig.ModelParametersURL != nil {
		effectiveModelParamsURL := *payload.FrameworkConfig.ModelParametersURL
		if effectiveModelParamsURL == "" {
			effectiveModelParamsURL = modelcatalog.DefaultModelParametersURL
		}
		if effectiveModelParamsURL != *frameworkConfig.ModelParametersURL {
			if effectiveModelParamsURL != modelcatalog.DefaultModelParametersURL {
				if err := checkURLAccessibility(effectiveModelParamsURL); err != nil {
					logger.Warn("failed to check the accessibility of the model parameters URL: %v", err)
					SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to check the accessibility of the model parameters URL: %v", err))
					return
				}
			}
			frameworkConfig.ModelParametersURL = &effectiveModelParamsURL
			shouldReloadFrameworkConfig = true
		}
	}
	if payload.FrameworkConfig.MCPLibraryURL != nil {
		effectiveMCPLibraryURL := *payload.FrameworkConfig.MCPLibraryURL
		if effectiveMCPLibraryURL == "" {
			effectiveMCPLibraryURL = modelcatalog.DefaultMCPLibraryURL
		}
		if frameworkConfig.MCPLibraryURL == nil || effectiveMCPLibraryURL != *frameworkConfig.MCPLibraryURL {
			frameworkConfig.MCPLibraryURL = &effectiveMCPLibraryURL
			shouldReloadFrameworkConfig = true
		}
	}
	if payload.FrameworkConfig.MCPLibrarySyncInterval != nil {
		syncInterval := *payload.FrameworkConfig.MCPLibrarySyncInterval
		if frameworkConfig.MCPLibrarySyncInterval == nil || syncInterval != *frameworkConfig.MCPLibrarySyncInterval {
			frameworkConfig.MCPLibrarySyncInterval = &syncInterval
			shouldReloadFrameworkConfig = true
		}
	}
	if payload.FrameworkConfig.LiveModelsSyncInterval != nil {
		syncInterval := *payload.FrameworkConfig.LiveModelsSyncInterval
		if frameworkConfig.LiveModelsSyncInterval == nil || syncInterval != *frameworkConfig.LiveModelsSyncInterval {
			frameworkConfig.LiveModelsSyncInterval = &syncInterval
			shouldReloadFrameworkConfig = true
		}
	}
	// Reload config if required
	if shouldReloadFrameworkConfig {
		var syncSeconds int64
		if frameworkConfig.PricingSyncInterval != nil {
			syncSeconds = *frameworkConfig.PricingSyncInterval
		} else {
			syncSeconds = int64(modelcatalog.DefaultSyncInterval.Seconds())
		}
		updatedFrameworkConfig := &framework.FrameworkConfig{
			Pricing: &modelcatalog.Config{
				PricingURL:             frameworkConfig.PricingURL,
				PricingSyncInterval:    &syncSeconds,
				ModelParametersURL:     frameworkConfig.ModelParametersURL,
				MCPLibraryURL:          frameworkConfig.MCPLibraryURL,
				MCPLibrarySyncInterval: frameworkConfig.MCPLibrarySyncInterval,
				LiveModelsSyncInterval: frameworkConfig.LiveModelsSyncInterval,
			},
		}
		// Publish the new config under the write lock: other request goroutines
		// read this pointer through LiveModelsSyncInterval and UpdateSyncConfig.
		// A whole new struct is swapped in rather than mutated in place, which is
		// what lets readers use the pointer after releasing the lock. Scoped to
		// the assignment alone — the store write and reload below take the read
		// lock themselves, and sync.RWMutex is not reentrant.
		h.store.Mu.Lock()
		h.store.FrameworkConfig = updatedFrameworkConfig
		h.store.Mu.Unlock()
		// Saving framework config
		if err := h.store.ConfigStore.UpdateFrameworkConfig(ctx, frameworkConfig); err != nil {
			logger.Warn("failed to save framework configuration: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save framework configuration: %v", err))
			return
		}
		// Reloading pricing manager
		h.configManager.UpdateSyncConfig(ctx)
	}
	// Checking auth config and trying to update if required
	if payload.AuthConfig != nil {
		// Getting current governance config
		authConfig, err := h.store.ConfigStore.GetAuthConfig(ctx)
		if err != nil {
			if !errors.Is(err, configstore.ErrNotFound) {
				logger.Warn("failed to get auth config from store: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get auth config from store: %v", err))
				return
			}
		}

		// Check if auth config has changed
		authChanged := false
		if authConfig == nil {
			// No existing config, any enabled state is a change
			if payload.AuthConfig.IsEnabled {
				authChanged = true
			}
		} else {
			// Compare with existing config using value comparison (not pointer comparison)
			// Password is considered changed when it was intentionally submitted —
			// ShouldPreserveStored() returns false for both plain values and secret refs.
			passwordChanged := payload.AuthConfig.AdminPassword != nil &&
				!payload.AuthConfig.AdminPassword.ShouldPreserveStored()
			usernameChanged := payload.AuthConfig.AdminUserName != nil &&
				!payload.AuthConfig.AdminUserName.Equals(authConfig.AdminUserName)
			if payload.AuthConfig.IsEnabled != authConfig.IsEnabled ||
				usernameChanged ||
				passwordChanged {
				authChanged = true
			}
		}

		if payload.AuthConfig.IsEnabled {
			// Initialize nil pointers to empty SecretVar to prevent nil-pointer dereference
			if payload.AuthConfig.AdminUserName == nil {
				payload.AuthConfig.AdminUserName = &schemas.SecretVar{}
			}
			if payload.AuthConfig.AdminPassword == nil {
				payload.AuthConfig.AdminPassword = &schemas.SecretVar{}
			}

			// Validate env variables are set if referenced
			if payload.AuthConfig.AdminUserName.IsFromSecret() && payload.AuthConfig.AdminUserName.GetValue() == "" {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("external reference %s for admin_username resolved to an empty value", payload.AuthConfig.AdminUserName.GetRawRef()))
				return
			}
			if payload.AuthConfig.AdminPassword.IsFromSecret() && payload.AuthConfig.AdminPassword.GetValue() == "" {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("external reference %s for admin_password resolved to an empty value", payload.AuthConfig.AdminPassword.GetRawRef()))
				return
			}

			if authConfig == nil && (payload.AuthConfig.AdminUserName.GetValue() == "" || payload.AuthConfig.AdminPassword.GetValue() == "") {
				SendError(ctx, fasthttp.StatusBadRequest, "auth username and password must be provided")
				return
			}
			// Fetching current Auth config
			if payload.AuthConfig.AdminUserName.GetValue() != "" {
				if payload.AuthConfig.AdminPassword.ShouldPreserveStored() {
					if authConfig == nil || authConfig.AdminPassword.GetValue() == "" {
						SendError(ctx, fasthttp.StatusBadRequest, "auth password must be provided")
						return
					}
					// Assuming that password hasn't been changed
					payload.AuthConfig.AdminPassword = authConfig.AdminPassword
				} else {
					// Password has been changed
					passwordPolicyFailures := getPasswordPolicyFailures(payload.AuthConfig.AdminPassword.GetValue())
					if len(passwordPolicyFailures) > 0 {
						SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("auth password must include %s", strings.Join(passwordPolicyFailures, ", ")))
						return
					}
					// We will hash the password
					hashedPassword, err := encrypt.Hash(payload.AuthConfig.AdminPassword.GetValue())
					if err != nil {
						logger.Warn("failed to hash password: %v", err)
						SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
						return
					}
					// Preserve env/vault reference metadata when storing hashed password
					if payload.AuthConfig.AdminPassword.IsFromSecret() {
						sv := *payload.AuthConfig.AdminPassword
						sv.Val = hashedPassword
						payload.AuthConfig.AdminPassword = &sv
					} else {
						payload.AuthConfig.AdminPassword = &schemas.SecretVar{Val: hashedPassword}
					}
				}
			}
			// Save auth config - this handles both first-time creation and updates
			err = h.configManager.UpdateAuthConfig(ctx, payload.AuthConfig)
			if err != nil {
				logger.Warn("failed to update auth config: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update auth config: %v", err))
				return
			}
		} else if authConfig != nil {
			// Auth is being disabled but there's an existing config - preserve credentials and update disabled state
			if payload.AuthConfig.AdminPassword.ShouldPreserveStored() {
				payload.AuthConfig.AdminPassword = authConfig.AdminPassword
			}
			if payload.AuthConfig.AdminUserName == nil || payload.AuthConfig.AdminUserName.GetValue() == "" {
				payload.AuthConfig.AdminUserName = authConfig.AdminUserName
			}
			err = h.configManager.UpdateAuthConfig(ctx, payload.AuthConfig)
			if err != nil {
				logger.Warn("failed to update auth config: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update auth config: %v", err))
				return
			}
		}

		// Flush all existing sessions if auth details have been changed
		if authChanged {
			if err := h.store.ConfigStore.FlushSessions(ctx); err != nil {
				logger.Warn("updated auth config but failed to flush existing sessions, please restart the server: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("updated auth config but failed to flush existing sessions, please restart the server: %v", err))
				return
			}
		}
		// Note: AuthMiddleware is updated via ServerCallbacks.UpdateAuthConfig (handled by BifrostHTTPServer)
	}

	// Set restart required flag if any restart-requiring configs changed
	if len(restartReasons) > 0 {
		reason := fmt.Sprintf("%s settings have been updated. A restart is required for changes to take full effect.", strings.Join(restartReasons, ", "))
		if err := h.store.ConfigStore.SetRestartRequiredConfig(ctx, &configstoreTables.RestartRequiredConfig{
			Required: true,
			Reason:   reason,
		}); err != nil {
			logger.Warn("failed to set restart required config: %v", err)
		}
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "configuration updated successfully",
	})
}

// forceSyncPricing triggers an immediate pricing sync and resets the pricing sync timer
func (h *ConfigHandler) forceSyncPricing(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	if err := h.configManager.ForceReloadPricing(ctx); err != nil {
		logger.Warn("failed to force pricing sync: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to force pricing sync: %v", err))
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "pricing synced successfully",
	})
}

// getProxyConfig handles GET /api/proxy-config - Get the current proxy configuration
func (h *ConfigHandler) getProxyConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}
	proxyConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get proxy config: %v", err))
		return
	}
	if proxyConfig == nil {
		// Return default empty config
		SendJSON(ctx, configstoreTables.GlobalProxyConfig{
			Enabled: false,
			Type:    network.GlobalProxyTypeHTTP,
		})
		return
	}
	// Redact password if present
	if proxyConfig.Password != "" {
		proxyConfig.Password = "<redacted>"
	}
	SendJSON(ctx, proxyConfig)
}

// updateProxyConfig handles PUT /api/proxy-config - Update the proxy configuration
func (h *ConfigHandler) updateProxyConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "config store not initialized")
		return
	}

	var payload configstoreTables.GlobalProxyConfig
	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	// Validate proxy config
	if payload.Enabled {
		// Validate proxy type
		switch payload.Type {
		case network.GlobalProxyTypeHTTP:
			// HTTP proxy is supported
			// Make sure the URL is provided
			if payload.URL == "" {
				SendError(ctx, fasthttp.StatusBadRequest, "proxy URL is required when proxy is enabled")
				return
			}
			// Validate timeout if provided
			if payload.Timeout < 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "proxy timeout must be non-negative")
				return
			}
		case network.GlobalProxyTypeSOCKS5, network.GlobalProxyTypeTCP:
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("proxy type %s is not yet supported", payload.Type))
			return
		default:
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid proxy type: %s", payload.Type))
			return
		}

		// Validate URL is provided when enabled
		if payload.URL == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "proxy URL is required when proxy is enabled")
			return
		}

		// Validate timeout if provided
		if payload.Timeout < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "proxy timeout must be non-negative")
			return
		}
	}

	// Handle password - if it's "<redacted>", keep the existing password
	if payload.Password == "<redacted>" {
		existingConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing proxy config: %v", err))
			return
		}
		if existingConfig != nil {
			payload.Password = existingConfig.Password
		} else {
			payload.Password = ""
		}
	}

	// Save proxy config
	if err := h.store.ConfigStore.UpdateProxyConfig(ctx, &payload); err != nil {
		logger.Warn("failed to save proxy configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save proxy configuration: %v", err))
		return
	}

	// Pulling the proxy config from the config store
	newProxyConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
	if err != nil {
		logger.Warn("failed to get proxy config from store: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get proxy config from store: %v", err))
		return
	}
	if newProxyConfig == nil {
		newProxyConfig = &configstoreTables.GlobalProxyConfig{
			Enabled:       false,
			Type:          network.GlobalProxyTypeHTTP,
			URL:           "",
			Username:      "",
			Password:      "",
			NoProxy:       "",
			Timeout:       0,
			SkipTLSVerify: false,
		}
	}

	// Reload proxy config in the server
	if err := h.configManager.ReloadProxyConfig(ctx, newProxyConfig); err != nil {
		logger.Warn("failed to reload proxy config: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload proxy config: %v", err))
		return
	}

	// Set restart required flag for proxy config changes
	if err := h.store.ConfigStore.SetRestartRequiredConfig(ctx, &configstoreTables.RestartRequiredConfig{
		Required: true,
		Reason:   "Proxy configuration has been updated. A restart is required for all changes to take full effect.",
	}); err != nil {
		logger.Warn("failed to set restart required config: %v", err)
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "proxy configuration updated successfully",
	})
}

// headerFilterConfigEqual compares two GlobalHeaderFilterConfig for equality
func headerFilterConfigEqual(a, b *configstoreTables.GlobalHeaderFilterConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return slices.Equal(a.Allowlist, b.Allowlist) && slices.Equal(a.Denylist, b.Denylist)
}

// validateHeaderFilterConfig validates that no exact security header names are in the allowlist or denylist
// and that wildcard patterns use valid syntax (only trailing * is supported).
// Wildcard patterns that would match security headers are allowed because security headers
// are unconditionally stripped at runtime regardless of configuration.
// Returns an error if any exact security headers are found or patterns are invalid.
func validateHeaderFilterConfig(config *configstoreTables.GlobalHeaderFilterConfig) error {
	if config == nil {
		return nil
	}

	// Validate pattern syntax and normalize entries (trim, lowercase, drop empties)
	filteredAllow := config.Allowlist[:0]
	for _, header := range config.Allowlist {
		h := strings.ToLower(strings.TrimSpace(header))
		if h == "" {
			continue
		}
		if idx := strings.Index(h, "*"); idx != -1 && idx != len(h)-1 {
			return fmt.Errorf("invalid pattern %q: wildcard (*) is only supported at the end of a pattern", h)
		}
		filteredAllow = append(filteredAllow, h)
	}
	config.Allowlist = filteredAllow
	filteredDeny := config.Denylist[:0]
	for _, header := range config.Denylist {
		h := strings.ToLower(strings.TrimSpace(header))
		if h == "" {
			continue
		}
		if idx := strings.Index(h, "*"); idx != -1 && idx != len(h)-1 {
			return fmt.Errorf("invalid pattern %q: wildcard (*) is only supported at the end of a pattern", h)
		}
		filteredDeny = append(filteredDeny, h)
	}
	config.Denylist = filteredDeny

	var foundSecurityHeaders []string

	// Check allowlist for exact security header names.
	// Wildcard patterns are allowed — security headers are always stripped at runtime
	// unconditionally in ctx.go, regardless of allowlist/denylist configuration.
	for _, header := range config.Allowlist {
		headerLower := strings.ToLower(strings.TrimSpace(header))
		if strings.Contains(headerLower, "*") {
			continue
		}
		if slices.Contains(securityHeaders, headerLower) {
			foundSecurityHeaders = append(foundSecurityHeaders, headerLower)
		}
	}

	// Check denylist for exact security header names.
	for _, header := range config.Denylist {
		headerLower := strings.ToLower(strings.TrimSpace(header))
		if strings.Contains(headerLower, "*") {
			continue
		}
		if slices.Contains(securityHeaders, headerLower) && !slices.Contains(foundSecurityHeaders, headerLower) {
			foundSecurityHeaders = append(foundSecurityHeaders, headerLower)
		}
	}

	if len(foundSecurityHeaders) > 0 {
		return fmt.Errorf("the following headers are not allowed to be configured: %s. These headers are security headers and are always blocked", strings.Join(foundSecurityHeaders, ", "))
	}

	return nil
}

// checkURLAccessibility verifies that the given URL is reachable.
// For file:// URLs it checks that the path exists on disk.
// For http(s):// URLs it performs a GET and expects a 200 OK.
func checkURLAccessibility(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme == "file" {
		info, err := os.Stat(parsed.Path)
		if err != nil {
			return fmt.Errorf("file not accessible: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file")
		}
		return nil
	}
	if err := bifrost.ValidateExternalURL(rawURL, true); err != nil {
		return fmt.Errorf("URL validation failed: %w", err)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
