package datasheet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rebuildTestStore builds a sqlite-backed config store in a temp dir,
// running the real migrations so delete/upsert paths behave like production.
func rebuildTestStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	cs, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	require.NoError(t, err)
	require.NotNil(t, cs)
	t.Cleanup(func() { _ = cs.Close(context.Background()) })
	return cs
}

// writeJSONFile writes content to a temp file and returns its path.
func writeJSONFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "datasheet.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return "file://" + path
}

// TestRebuildFromURL_ReseedsDatabaseAndDropsOrphans verifies the core rebuild
// contract: stale rows that no longer appear in the datasheets are removed,
// rows that still appear are overwritten with the new values, and the
// in-memory indexes reflect the fresh feed.
func TestRebuildFromURL_ReseedsDatabaseAndDropsOrphans(t *testing.T) {
	ctx := context.Background()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	cs := rebuildTestStore(t)

	staleCost := 999.0
	require.NoError(t, cs.UpsertModelPricesBatch(ctx, []configstoreTables.TableModelPricing{
		// Orphan: absent from the fixture datasheets, must disappear.
		{Model: "orphan-model", Provider: "openai", Mode: "chat", InputCostPerToken: &staleCost},
		// Outdated: present in fixtures with a different cost, must be overwritten.
		{Model: "gpt-4o", Provider: "openai", Mode: "chat", InputCostPerToken: &staleCost},
	}))
	require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "orphan-model", Data: `{"supported_endpoints":["/v1/chat/completions"]}`},
	}))

	store := New(cs, logger, Config{
		URL:                fileURL(t, "testdata/pricing.json"),
		ModelParametersURL: fileURL(t, "testdata/model-parameters.json"),
	})
	require.NoError(t, store.RebuildFromURL(ctx))

	prices, err := cs.GetModelPrices(ctx)
	require.NoError(t, err)
	for _, p := range prices {
		if p.Model == "orphan-model" {
			t.Fatalf("orphan pricing row survived rebuild: %+v", p)
		}
	}

	var gpt4o *configstoreTables.TableModelPricing
	for i := range prices {
		if prices[i].Model == "gpt-4o" && prices[i].Provider == "openai" && prices[i].Mode == "chat" {
			gpt4o = &prices[i]
		}
	}
	require.NotNil(t, gpt4o, "expected gpt-4o pricing row after rebuild")
	require.NotNil(t, gpt4o.InputCostPerToken)
	assert.InDelta(t, 2.5e-06, *gpt4o.InputCostPerToken, 1e-12)

	params, err := cs.GetModelParameters(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, params)
	for _, p := range params {
		if p.Model == "orphan-model" {
			t.Fatalf("orphan model parameters row survived rebuild: %+v", p)
		}
	}

	if _, err := cs.GetModelParametersByModel(ctx, "orphan-model"); err == nil {
		t.Fatal("expected orphan-model parameters lookup to fail after rebuild")
	}

	supported := store.GetSupportedParameters("gpt-4o")
	for _, want := range []string{"temperature", "tools"} {
		if !slices.Contains(supported, want) {
			t.Fatalf("expected rebuilt supported parameters for gpt-4o to contain %q, got %v", want, supported)
		}
	}
	if !store.IsRequestTypeSupported("gpt-4o", schemas.ChatCompletionRequest) {
		t.Fatal("expected gpt-4o to support chat_completion after rebuild")
	}
}

// TestRebuildFromURL_RefusesEmptyDatasheets verifies the safety gate: when a
// datasheet parses to zero records, nothing is deleted and the existing rows
// survive untouched.
func TestRebuildFromURL_RefusesEmptyDatasheets(t *testing.T) {
	ctx := context.Background()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	cs := rebuildTestStore(t)

	cost := 0.000001
	require.NoError(t, cs.UpsertModelPricesBatch(ctx, []configstoreTables.TableModelPricing{
		{Model: "keep-me", Provider: "openai", Mode: "chat", InputCostPerToken: &cost},
	}))
	require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "keep-me", Data: `{"supported_endpoints":["/v1/chat/completions"]}`},
	}))

	store := New(cs, logger, Config{
		URL:                writeJSONFile(t, `{}`),
		ModelParametersURL: writeJSONFile(t, `{}`),
	})
	err := store.RebuildFromURL(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	prices, err := cs.GetModelPrices(ctx)
	require.NoError(t, err)
	require.Len(t, prices, 1)
	assert.Equal(t, "keep-me", prices[0].Model)

	kept, err := cs.GetModelParametersByModel(ctx, "keep-me")
	require.NoError(t, err)
	require.NotNil(t, kept)
}

// TestRebuildFromURL_RefusesUnusableParams verifies that valid JSON carrying
// no usable capability record is also rejected before any delete happens.
func TestRebuildFromURL_RefusesUnusableParams(t *testing.T) {
	ctx := context.Background()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	cs := rebuildTestStore(t)

	cost := 0.000001
	require.NoError(t, cs.UpsertModelPricesBatch(ctx, []configstoreTables.TableModelPricing{
		{Model: "keep-me", Provider: "openai", Mode: "chat", InputCostPerToken: &cost},
	}))

	store := New(cs, logger, Config{
		URL:                fileURL(t, "testdata/pricing.json"),
		ModelParametersURL: writeJSONFile(t, `{"ghost-model": {}}`),
	})
	err := store.RebuildFromURL(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable records")

	prices, err := cs.GetModelPrices(ctx)
	require.NoError(t, err)
	require.Len(t, prices, 1)
	assert.Equal(t, "keep-me", prices[0].Model)
}

// TestRebuildFromURL_RequiresConfigStore verifies rebuild refuses to run in
// memory-only mode, where a delete would have nowhere to seed back from.
func TestRebuildFromURL_RequiresConfigStore(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	store := New(nil, logger, Config{
		URL:                fileURL(t, "testdata/pricing.json"),
		ModelParametersURL: fileURL(t, "testdata/model-parameters.json"),
	})
	err := store.RebuildFromURL(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "without a config store"))
}
