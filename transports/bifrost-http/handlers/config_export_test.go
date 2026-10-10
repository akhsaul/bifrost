package handlers

import (
	"encoding/json"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func init() {
	l := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	lib.SetLogger(l)
	SetLogger(l)
}

func TestExportConfig_StructureAndDefaults(t *testing.T) {
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  500,
			LogRetentionDays: 90,
			Compat: configstore.CompatConfig{
				ConvertTextToChat: true,
			},
		},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {
				Keys: []schemas.Key{
					{
						ID:     "key-1",
						Name:   "openai-primary",
						Value:  *schemas.NewSecretVar("env.OPENAI_API_KEY"),
						Weight: 1,
					},
				},
			},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/config/export")

	handler.exportConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}

	var result map[string]any
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("failed to unmarshal exported config JSON: %v", err)
	}

	// Verify top-level required fields
	if result["$schema"] != "https://www.getbifrost.ai/schema" {
		t.Errorf("expected $schema https://www.getbifrost.ai/schema, got %v", result["$schema"])
	}
	if result["version"] != float64(2) {
		t.Errorf("expected version 2, got %v", result["version"])
	}
	if result["source_of_truth"] != "split" {
		t.Errorf("expected source_of_truth split, got %v", result["source_of_truth"])
	}

	// Verify server defaults
	server, ok := result["server"].(map[string]any)
	if !ok {
		t.Fatalf("expected server object, got %T", result["server"])
	}
	if server["read_buffer_size"] != float64(65536) {
		t.Errorf("expected read_buffer_size 65536, got %v", server["read_buffer_size"])
	}

	// Verify client defaults and overridden values
	client, ok := result["client"].(map[string]any)
	if !ok {
		t.Fatalf("expected client object, got %T", result["client"])
	}
	if client["initial_pool_size"] != float64(500) {
		t.Errorf("expected initial_pool_size 500, got %v", client["initial_pool_size"])
	}
	if client["log_retention_days"] != float64(90) {
		t.Errorf("expected log_retention_days 90, got %v", client["log_retention_days"])
	}
	if client["enable_logging"] != true {
		t.Errorf("expected enable_logging true, got %v", client["enable_logging"])
	}
	if client["routing_chain_max_depth"] != float64(10) {
		t.Errorf("expected routing_chain_max_depth 10, got %v", client["routing_chain_max_depth"])
	}

	// Verify providers structure
	providers, ok := result["providers"].(map[string]any)
	if !ok {
		t.Fatalf("expected providers object, got %T", result["providers"])
	}
	openai, ok := providers["openai"].(map[string]any)
	if !ok {
		t.Fatalf("expected openai provider object, got %T", providers["openai"])
	}
	keys, ok := openai["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("expected 1 key for openai, got %v", openai["keys"])
	}
	key := keys[0].(map[string]any)
	if key["value"] != "env.OPENAI_API_KEY" {
		t.Errorf("expected value env.OPENAI_API_KEY, got %v", key["value"])
	}
	if key["id"] != nil {
		t.Errorf("expected key id to be omitted from exported config, got %v", key["id"])
	}

	// Verify provider required fields always exported
	if openai["concurrency_and_buffer_size"] == nil {
		t.Errorf("expected concurrency_and_buffer_size to be exported")
	}
	if openai["network_config"] == nil {
		t.Errorf("expected network_config to be exported")
	}
	if openai["send_back_raw_request"] == nil {
		t.Errorf("expected send_back_raw_request to be exported")
	}
	if openai["send_back_raw_response"] == nil {
		t.Errorf("expected send_back_raw_response to be exported")
	}
	if openai["store_raw_request_response"] == nil {
		t.Errorf("expected store_raw_request_response to be exported")
	}

	// Standard providers never carry custom_provider_config.
	if _, exists := openai["custom_provider_config"]; exists {
		t.Errorf("expected no custom_provider_config for standard provider openai, got %v", openai["custom_provider_config"])
	}

	// Without a ConfigStore, export reports config_store disabled.
	cs, ok := result["config_store"].(map[string]any)
	if !ok {
		t.Fatalf("expected config_store object, got %T", result["config_store"])
	}
	if cs["enabled"] != false {
		t.Errorf("expected config_store.enabled false, got %v", cs["enabled"])
	}
}

func TestExportConfig_CustomProviderAllowedRequests(t *testing.T) {
	customName := schemas.ModelProvider("my-custom-test")
	newStore := func(cpc *schemas.CustomProviderConfig) *lib.Config {
		return &lib.Config{
			ClientConfig: &configstore.ClientConfig{
				InitialPoolSize:  1000,
				LogRetentionDays: 365,
			},
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				customName: {
					Keys: []schemas.Key{
						{
							ID:     "key-tf-1",
							Name:   "custom-primary",
							Value:  *schemas.NewSecretVar("env.CUSTOM_API_KEY"),
							Weight: 1,
						},
					},
					CustomProviderConfig: cpc,
				},
			},
		}
	}
	export := func(store *lib.Config) map[string]any {
		handler := NewConfigHandler(nil, store)
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/config/export")
		handler.exportConfig(ctx)
		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
		}
		var result map[string]any
		if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
			t.Fatalf("failed to unmarshal exported config JSON: %v", err)
		}
		providers, ok := result["providers"].(map[string]any)
		if !ok {
			t.Fatalf("expected providers object, got %T", result["providers"])
		}
		custom, ok := providers[string(customName)].(map[string]any)
		if !ok {
			t.Fatalf("expected custom provider object, got %T", providers[string(customName)])
		}
		return custom
	}

	// AllowedRequests set -> written verbatim.
	withAR := export(newStore(&schemas.CustomProviderConfig{
		BaseProviderType: schemas.OpenAI,
		AllowedRequests: &schemas.AllowedRequests{
			ChatCompletion:       true,
			ChatCompletionStream: true,
			ListModels:           true,
		},
	}))
	cpc, ok := withAR["custom_provider_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom_provider_config object, got %T", withAR["custom_provider_config"])
	}
	if cpc["base_provider_type"] != "openai" {
		t.Errorf("expected base_provider_type openai, got %v", cpc["base_provider_type"])
	}
	ar, ok := cpc["allowed_requests"].(map[string]any)
	if !ok {
		t.Fatalf("expected allowed_requests object, got %T", cpc["allowed_requests"])
	}
	if ar["chat_completion"] != true {
		t.Errorf("expected chat_completion true, got %v", ar["chat_completion"])
	}
	if ar["chat_completion_stream"] != true {
		t.Errorf("expected chat_completion_stream true, got %v", ar["chat_completion_stream"])
	}
	if ar["list_models"] != true {
		t.Errorf("expected list_models true, got %v", ar["list_models"])
	}
	if ar["realtime"] != false {
		t.Errorf("expected realtime false (explicit), got %v", ar["realtime"])
	}

	// AllowedRequests nil -> omit allowed_requests, keep the rest.
	nilAR := export(newStore(&schemas.CustomProviderConfig{
		BaseProviderType: schemas.OpenAI,
	}))
	cpcNil, ok := nilAR["custom_provider_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom_provider_config object, got %T", nilAR["custom_provider_config"])
	}
	if _, exists := cpcNil["allowed_requests"]; exists {
		t.Errorf("expected allowed_requests to be omitted when nil, got %v", cpcNil["allowed_requests"])
	}
	if cpcNil["base_provider_type"] != "openai" {
		t.Errorf("expected base_provider_type openai, got %v", cpcNil["base_provider_type"])
	}

	// No CustomProviderConfig at all -> omit the whole key.
	noCPC := export(newStore(nil))
	if _, exists := noCPC["custom_provider_config"]; exists {
		t.Errorf("expected custom_provider_config to be omitted when nil, got %v", noCPC["custom_provider_config"])
	}
}

func TestExportConfig_StandardProviderCustomConfigOmitted(t *testing.T) {
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  1000,
			LogRetentionDays: 365,
		},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			// A stale custom block on a standard provider must not leak out.
			schemas.OpenAI: {
				Keys: []schemas.Key{
					{
						ID:     "key-std-1",
						Name:   "openai-primary",
						Value:  *schemas.NewSecretVar("env.OPENAI_API_KEY"),
						Weight: 1,
					},
				},
				CustomProviderConfig: &schemas.CustomProviderConfig{
					BaseProviderType: schemas.OpenAI,
					AllowedRequests: &schemas.AllowedRequests{
						ChatCompletion: true,
					},
				},
			},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/config/export")

	handler.exportConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}

	var result map[string]any
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("failed to unmarshal exported config JSON: %v", err)
	}

	providers, ok := result["providers"].(map[string]any)
	if !ok {
		t.Fatalf("expected providers object, got %T", result["providers"])
	}
	openai, ok := providers["openai"].(map[string]any)
	if !ok {
		t.Fatalf("expected openai provider object, got %T", providers["openai"])
	}
	if _, exists := openai["custom_provider_config"]; exists {
		t.Errorf("expected custom_provider_config to be omitted for standard provider, got %v", openai["custom_provider_config"])
	}
}

func TestExportConfig_FrameworkConfigHashOmitted(t *testing.T) {
	defaultSync := int64(86400)
	dummyStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize: 1000,
		},
		FrameworkConfig: &framework.FrameworkConfig{
			Pricing: &modelcatalog.Config{
				PricingSyncInterval: &defaultSync,
			},
		},
	}
	h := NewConfigHandler(nil, dummyStore)

	fc := &configstoreTables.TableFrameworkConfig{
		ID:                  10,
		ConfigHash:          "test_hash_99",
		PricingSyncInterval: &defaultSync,
	}

	exported := formatExportFrameworkConfig(fc)
	pricing, ok := exported["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("expected pricing map")
	}
	if pricing["id"] != nil {
		t.Errorf("expected id to be omitted")
	}
	// config_hash is recomputed at load time; export must not emit it.
	if _, exists := pricing["config_hash"]; exists {
		t.Errorf("expected config_hash to be omitted, got %v", pricing["config_hash"])
	}
	_ = h
}

func TestExportConfig_StorageBlocks(t *testing.T) {
	sqlitePath := "/tmp/bifrost-test-config.db"
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  1000,
			LogRetentionDays: 365,
		},
		ConfigStoreConfig: &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypePostgres,
			Config: &configstore.PostgresConfig{
				Host:     schemas.NewSecretVar("env.PG_HOST"),
				Port:     schemas.NewSecretVar("env.PG_PORT"),
				User:     schemas.NewSecretVar("env.PG_USER"),
				Password: schemas.NewSecretVar("env.PG_PASSWORD"),
				DBName:   schemas.NewSecretVar("env.PG_DB"),
				SSLMode:  schemas.NewSecretVar("disable"),
			},
		},
		VectorStoreConfig: &vectorstore.Config{
			Enabled: true,
			Type:    vectorstore.VectorStoreTypeRedis,
			Config: vectorstore.RedisConfig{
				Addr:     schemas.NewSecretVar("env.REDIS_ADDR"),
				Password: schemas.NewSecretVar("env.REDIS_PASSWORD"),
			},
		},
		LogsStoreConfig: &logstore.Config{
			Enabled: true,
			Type:    logstore.LogStoreTypeSQLite,
			Config:  &logstore.SQLiteConfig{Path: sqlitePath},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/config/export")

	handler.exportConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}

	var result map[string]any
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("failed to unmarshal exported config JSON: %v", err)
	}

	// config_store renders the full connection block, not just enabled.
	cs, ok := result["config_store"].(map[string]any)
	if !ok {
		t.Fatalf("expected config_store object, got %T", result["config_store"])
	}
	if cs["enabled"] != true {
		t.Errorf("expected config_store.enabled true, got %v", cs["enabled"])
	}
	if cs["type"] != "postgres" {
		t.Errorf("expected config_store.type postgres, got %v", cs["type"])
	}
	cscfg, ok := cs["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected config_store.config object, got %T", cs["config"])
	}
	if cscfg["host"] != "env.PG_HOST" {
		t.Errorf("expected host env.PG_HOST, got %v", cscfg["host"])
	}
	if cscfg["password"] != "env.PG_PASSWORD" {
		t.Errorf("expected password env.PG_PASSWORD, got %v", cscfg["password"])
	}

	// vector_store.config must be an object, not a JSON string.
	vs, ok := result["vector_store"].(map[string]any)
	if !ok {
		t.Fatalf("expected vector_store object, got %T", result["vector_store"])
	}
	if vs["type"] != "redis" {
		t.Errorf("expected vector_store.type redis, got %v", vs["type"])
	}
	vscfg, ok := vs["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected vector_store.config object (not string), got %T", vs["config"])
	}
	if vscfg["addr"] != "env.REDIS_ADDR" {
		t.Errorf("expected addr env.REDIS_ADDR, got %v", vscfg["addr"])
	}
	if vscfg["password"] != "env.REDIS_PASSWORD" {
		t.Errorf("expected password env.REDIS_PASSWORD, got %v", vscfg["password"])
	}

	// logs_store renders sqlite path block.
	ls, ok := result["logs_store"].(map[string]any)
	if !ok {
		t.Fatalf("expected logs_store object, got %T", result["logs_store"])
	}
	if ls["type"] != "sqlite" {
		t.Errorf("expected logs_store.type sqlite, got %v", ls["type"])
	}
	lscfg, ok := ls["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected logs_store.config object, got %T", ls["config"])
	}
	if lscfg["path"] != sqlitePath {
		t.Errorf("expected path %s, got %v", sqlitePath, lscfg["path"])
	}
}

func TestExportConfig_NoConfigHashAnywhere(t *testing.T) {
	active := true
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  1000,
			LogRetentionDays: 365,
		},
		FrameworkConfig: &framework.FrameworkConfig{
			Pricing: &modelcatalog.Config{},
		},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.ModelProvider("my-custom-test"): {
				Keys: []schemas.Key{
					{
						ID:     "key-1",
						Name:   "custom-primary",
						Value:  *schemas.NewSecretVar("env.CUSTOM_API_KEY"),
						Weight: 1,
					},
				},
				CustomProviderConfig: &schemas.CustomProviderConfig{
					BaseProviderType: schemas.OpenAI,
					AllowedRequests:  &schemas.AllowedRequests{ChatCompletion: true},
				},
			},
		},
		GovernanceConfig: &configstore.GovernanceConfig{
			VirtualKeys: []configstoreTables.TableVirtualKey{
				{ID: "vk-1", Name: "vk-one", Value: *schemas.NewSecretVar("vk-secret"), IsActive: &active, ConfigHash: "stale-hash-vk"},
			},
			Budgets: []configstoreTables.TableBudget{
				{ID: "b-1", MaxLimit: 10, ResetDuration: "1d", ConfigHash: "stale-hash-budget"},
			},
			RateLimits: []configstoreTables.TableRateLimit{
				{ID: "rl-1", ConfigHash: "stale-hash-rl"},
			},
			RoutingRules: []configstoreTables.TableRoutingRule{
				{ID: "rr-1", Name: "rule-one", CelExpression: "true", ConfigHash: "stale-hash-rr"},
			},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/config/export")

	handler.exportConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}

	var result map[string]any
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("failed to unmarshal exported config JSON: %v", err)
	}

	var paths []string
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if k == "config_hash" {
					paths = append(paths, path+"."+k)
				}
				walk(child, path+"."+k)
			}
		case []any:
			for i, item := range node {
				walk(item, path+"[]")
				_ = i
			}
		}
	}
	walk(result, "$")
	if len(paths) > 0 {
		t.Errorf("expected no config_hash keys in export, found at: %v", paths)
	}

	_ = framework.FrameworkConfig{}
	_ = modelcatalog.Config{}
}

// TestExportConfig_RoundTripThroughLoader feeds the export output back through
// ConfigData.UnmarshalJSON — the same path config.json takes at boot. Any
// SecretVar-object leak, JSON-string blob, or shape mismatch fails here.
func TestExportConfig_RoundTripThroughLoader(t *testing.T) {
	sqlitePath := "/tmp/bifrost-rt-config.db"
	active := true
	inMemoryStore := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			InitialPoolSize:  1000,
			LogRetentionDays: 365,
		},
		ConfigStoreConfig: &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config:  &configstore.SQLiteConfig{Path: sqlitePath},
		},
		VectorStoreConfig: &vectorstore.Config{
			Enabled: true,
			Type:    vectorstore.VectorStoreTypeRedis,
			Config: vectorstore.RedisConfig{
				Addr:     schemas.NewSecretVar("env.REDIS_ADDR"),
				Password: schemas.NewSecretVar("env.REDIS_PASSWORD"),
			},
		},
		LogsStoreConfig: &logstore.Config{
			Enabled: true,
			Type:    logstore.LogStoreTypeSQLite,
			Config:  &logstore.SQLiteConfig{Path: "/tmp/bifrost-rt-logs.db"},
		},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {
				Keys: []schemas.Key{
					{ID: "k1", Name: "openai-primary", Value: *schemas.NewSecretVar("env.OPENAI_API_KEY"), Weight: 1},
				},
			},
			schemas.ModelProvider("my-custom-rt"): {
				Keys: []schemas.Key{
					{ID: "k2", Name: "custom-primary", Value: *schemas.NewSecretVar("env.CUSTOM_API_KEY"), Weight: 1},
				},
				CustomProviderConfig: &schemas.CustomProviderConfig{
					BaseProviderType: schemas.OpenAI,
					AllowedRequests:  &schemas.AllowedRequests{ChatCompletion: true, ListModels: true},
				},
			},
		},
		GovernanceConfig: &configstore.GovernanceConfig{
			VirtualKeys: []configstoreTables.TableVirtualKey{
				{ID: "vk-1", Name: "vk-one", Value: *schemas.NewSecretVar("vk-secret"), IsActive: &active},
			},
			Budgets: []configstoreTables.TableBudget{
				{ID: "b-1", MaxLimit: 10, ResetDuration: "1d"},
			},
		},
	}

	handler := NewConfigHandler(nil, inMemoryStore)
	ctx := &fasthttp.RequestCtx{}
	handler.exportConfig(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
	}
	body := append([]byte(nil), ctx.Response.Body()...)

	var cd lib.ConfigData
	if err := json.Unmarshal(body, &cd); err != nil {
		t.Fatalf("export output failed to load as config.json: %v\n%s", err, body)
	}
	if cd.ConfigStoreConfig == nil || cd.ConfigStoreConfig.Type != configstore.ConfigStoreTypeSQLite {
		t.Errorf("config_store did not round-trip: %+v", cd.ConfigStoreConfig)
	}
	if cd.VectorStoreConfig == nil || cd.VectorStoreConfig.Type != vectorstore.VectorStoreTypeRedis {
		t.Errorf("vector_store did not round-trip: %+v", cd.VectorStoreConfig)
	} else if rc, ok := cd.VectorStoreConfig.Config.(vectorstore.RedisConfig); !ok {
		t.Errorf("vector_store backend wrong type %T", cd.VectorStoreConfig.Config)
	} else if rc.Addr == nil || rc.Addr.GetRawRef() != "env.REDIS_ADDR" {
		t.Errorf("redis addr ref lost: %+v", rc.Addr)
	}
	if cd.LogsStoreConfig == nil {
		t.Errorf("logs_store did not round-trip")
	}
	if _, ok := cd.Providers["my-custom-rt"]; !ok {
		t.Errorf("custom provider lost in round-trip: %v", cd.Providers)
	}
	if _, ok := cd.Providers["openai"]; !ok {
		t.Errorf("openai provider lost in round-trip")
	}
	if os.Getenv("PATH") == "" {
		t.Errorf("expected PATH to be set in test env")
	}
}
